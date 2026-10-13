package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
)

type stepRig struct {
	la     *LocalAuth
	clk    *fakeClock
	h      http.Handler
	cookie *http.Cookie
}

const stepPW = "pw-pw-pw-pw-pw"

func newStepRig(t *testing.T) *stepRig {
	t.Helper()
	ctx := context.Background()
	clk := newFakeClock()
	la := newLocal(t, "http://mm.example.com")
	la.UseClock(clk.Now)
	if err := la.SetPassword(ctx, "alice", stepPW); err != nil {
		t.Fatal(err)
	}
	cv, err := la.Login(ctx, "alice", stepPW)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/auth/", la.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := Middleware(config.Config{AuthMode: "local", PublicURL: "http://mm.example.com"}, config.Secrets{Key: key}, fakeTokens{}, la, nil)(mux)
	return &stepRig{la: la, clk: clk, h: h, cookie: &http.Cookie{Name: SessionCookie, Value: cv}}
}

func (s *stepRig) do(method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.AddCookie(s.cookie)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec
}

func (s *stepRig) stepUp(next, pw string) *httptest.ResponseRecorder {
	return s.do("POST", "/auth/step-up", url.Values{"password": {pw}, "next": {next}}.Encode())
}

func TestLocalStepUp_HumanPaths(t *testing.T) {
	s := newStepRig(t)

	if rec := s.do("GET", "/api/tracks", ""); rec.Code != 200 {
		t.Fatalf("non-human path needs no step-up: %d", rec.Code)
	}
	rec := s.do("GET", "/human/api/tokens", "")
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "STEP_UP_REQUIRED") {
		t.Fatalf("fresh login: %d %s", rec.Code, rec.Body)
	}
	rec = s.do("GET", "/human/tokens", "")
	if rec.Code != 303 || rec.Header().Get("Location") != "/auth/step-up?next=%2Fhuman%2Ftokens" {
		t.Fatalf("HTML: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// the form
	rec = s.do("GET", "/auth/step-up?next=/human/devices", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `value="/human/devices"`) ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("form: %d %s", rec.Code, rec.Body)
	}

	// wrong password: 401 page, still locked
	if rec = s.stepUp("/human/tokens", "wrong"); rec.Code != 401 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	if rec = s.do("GET", "/human/api/tokens", ""); rec.Code != 403 {
		t.Fatalf("still locked: %d", rec.Code)
	}

	rec = s.stepUp("/human/devices", stepPW)
	if rec.Code != 303 || rec.Header().Get("Location") != "/human/devices" {
		t.Fatalf("step-up: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec = s.do("GET", "/human/api/tokens", ""); rec.Code != 200 {
		t.Fatalf("after step-up: %d %s", rec.Code, rec.Body)
	}
	s.clk.Advance(10 * time.Minute)
	if rec = s.do("GET", "/human/api/tokens", ""); rec.Code != 200 {
		t.Fatalf("at exactly 10 min: %d", rec.Code)
	}
	s.clk.Advance(time.Second)
	if rec = s.do("GET", "/human/api/tokens", ""); rec.Code != 403 {
		t.Fatalf("10 min 01 s: %d", rec.Code)
	}
}

func TestLocalStepUp_NextIsConfined(t *testing.T) {
	s := newStepRig(t)
	for _, next := range []string{"//evil.example", "https://evil.example/human/x", "/human//x", `/human/\x`, "/human/x\r\nSet-Cookie: a=b",
		"/human/x\x00", "/api/tracks", "", "human/x", "/human/\u0085x"} {
		rec := s.stepUp(next, stepPW)
		if rec.Code != 303 || rec.Header().Get("Location") != "/human/tokens" {
			t.Errorf("next %q -> %d %q", next, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := s.stepUp("/human/devices?x=1", stepPW)
	if rec.Header().Get("Location") != "/human/devices?x=1" {
		t.Errorf("query kept: %q", rec.Header().Get("Location"))
	}
}

func TestLocalStepUp_Errors(t *testing.T) {
	s := newStepRig(t)
	// cross-site POST refused
	r := httptest.NewRequest("POST", "/auth/step-up", strings.NewReader("password="+stepPW))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(s.cookie)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "CROSS_ORIGIN") {
		t.Fatalf("cross-site: %d %s", rec.Code, rec.Body)
	}
	// no session at all: the middleware sends the browser to the login page
	r = httptest.NewRequest("POST", "/auth/step-up", strings.NewReader("password="+stepPW))
	rec = httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	if rec.Code != 303 || rec.Header().Get("Location") != "/auth/login" {
		t.Fatalf("anonymous: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// LocalAuth.StepUp itself
	ctx := context.Background()
	if err := s.la.StepUp(ctx, "nope", stepPW); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unknown session: %v", err)
	}
	if err := s.la.StepUp(ctx, s.cookie.Value, "bad"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("bad password: %v", err)
	}
	if err := s.la.StepUp(ctx, s.cookie.Value, stepPW); err != nil {
		t.Fatal(err)
	}
	p, err := s.la.VerifySession(ctx, s.cookie.Value)
	if err != nil || p.StepUpAt.IsZero() || !s.la.StepUpFresh(p) {
		t.Fatalf("StepUpAt not recorded: %+v %v", p, err)
	}
	s.clk.Advance(stepUpWindow + time.Second)
	if s.la.StepUpFresh(p) {
		t.Fatal("stale step-up reported fresh")
	}
	// an expired session cannot step up
	s.clk.Advance(24 * time.Hour)
	if err := s.la.StepUp(ctx, s.cookie.Value, stepPW); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session: %v", err)
	}
}

func TestLocalStepUp_FailureDelayAndNotRevealed(t *testing.T) {
	s := newStepRig(t)
	s.la.FailDelay = 50 * time.Millisecond
	start := time.Now()
	rec := s.stepUp("/human/tokens", "wrong")
	if time.Since(start) < 50*time.Millisecond || rec.Code != 401 || strings.Contains(rec.Body.String(), "wrong") {
		t.Fatalf("delay/echo: %v %d", time.Since(start), rec.Code)
	}
}
