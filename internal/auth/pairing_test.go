package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
)

type pairRig struct {
	h      http.Handler
	ds     *DeviceStore
	clk    *fakeClock
	paired atomic.Int32
	last   atomic.Pointer[DeviceInfo]
	allow  atomic.Int32 // number of POST /auth/pair admitted so far
	limit  int32
}

func newPairRig(t *testing.T, mode string, sessions SessionVerifier) *pairRig {
	t.Helper()
	ds, clk := newDevices(t)
	pr := &pairRig{ds: ds, clk: clk, limit: 10}
	pr.h = PairingHandler(PairingOptions{
		Cfg:      config.Config{AuthMode: mode, PublicURL: "https://mm.example.com"},
		Devices:  ds,
		Sessions: sessions,
		Allow: func() (bool, time.Duration) {
			if pr.allow.Add(1) > pr.limit {
				return false, 42 * time.Second
			}
			return true, 0
		},
		OnPaired:  func(_ context.Context, d DeviceInfo) { pr.paired.Add(1); pr.last.Store(&d) },
		FailDelay: time.Millisecond,
	})
	return pr
}

func (p *pairRig) req(method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Cookie" {
			r.Header.Add("Cookie", hdr[i+1])
		} else {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, r)
	return rec
}

func checkPageHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("Content-Security-Policy") != "default-src 'self'; frame-ancestors 'none'" ||
		h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers: %v", h)
	}
}

func TestRateLimit_PairEndpoint(t *testing.T) {
	t.Parallel()
	pr := newPairRig(t, "proxy", nil)
	ctx := context.Background()

	// GET /pair: form, script, never echoes a code
	rec := pr.req("GET", "/pair?code=ABCD-EFGH", "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `autocomplete="one-time-code"`) || !strings.Contains(body, `maxlength="9"`) ||
		!strings.Contains(body, `<script src="/pair.js" defer>`) || strings.Contains(body, "ABCD") {
		t.Fatalf("GET /pair: %d %s", rec.Code, body)
	}
	checkPageHeaders(t, rec)
	js := pr.req("GET", "/pair.js", "")
	if js.Code != 200 || !strings.Contains(js.Header().Get("Content-Type"), "javascript") || !strings.Contains(js.Body.String(), "replaceState") ||
		!strings.Contains(js.Body.String(), "location.hash") {
		t.Fatalf("pair.js: %d %q", js.Code, js.Body)
	}
	checkPageHeaders(t, js)

	// wrong code: 401 page after FailDelay; the code is not echoed
	start := time.Now()
	pr2 := newPairRig(t, "proxy", nil)
	pr2.h = PairingHandler(PairingOptions{Cfg: config.Config{AuthMode: "proxy"}, Devices: pr.ds, FailDelay: 60 * time.Millisecond})
	rec = pr2.req("POST", "/auth/pair", url.Values{"code": {"ZZZZ-ZZZZ"}}.Encode())
	if rec.Code != 401 || time.Since(start) < 60*time.Millisecond || !strings.Contains(rec.Body.String(), "Code not valid or expired") ||
		strings.Contains(rec.Body.String(), "ZZZZ") || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body)
	}
	checkPageHeaders(t, rec)

	// cross-site POST refused
	code, _, _ := pr.ds.CreatePairingCode(ctx, human, "Phone")
	form := url.Values{"code": {code}}.Encode()
	rec = pr.req("POST", "/auth/pair", form, "Sec-Fetch-Site", "cross-site")
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "CROSS_ORIGIN") || pr.paired.Load() != 0 {
		t.Fatalf("cross-site: %d %s", rec.Code, rec.Body)
	}

	// success: cookie attributes as C26, 303 /, OnPaired once
	rec = pr.req("POST", "/auth/pair", form, "User-Agent", "TestUA/1")
	if rec.Code != 303 || rec.Header().Get("Location") != "/" || pr.paired.Load() != 1 {
		t.Fatalf("pair: %d %q paired=%d", rec.Code, rec.Header().Get("Location"), pr.paired.Load())
	}
	var ck *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == DeviceCookie {
			ck = c
		}
	}
	if ck == nil || !cookieRE.MatchString(ck.Value) || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode ||
		ck.Path != "/" || ck.MaxAge != int(DeviceTTL.Seconds()) {
		t.Fatalf("cookie %+v", ck)
	}
	if d := pr.last.Load(); d == nil || d.Name != "Phone" || d.UserAgent != "TestUA/1" {
		t.Fatalf("OnPaired device %+v", d)
	}
	if _, err := pr.ds.VerifyDevice(ctx, ck.Value); err != nil {
		t.Fatal(err)
	}
	// the code is single use
	if rec = pr.req("POST", "/auth/pair", form); rec.Code != 401 || pr.paired.Load() != 1 {
		t.Fatalf("reuse: %d", rec.Code)
	}

	// oversized body and rate limit
	if rec = pr.req("POST", "/auth/pair", "code="+strings.Repeat("A", 5000)); rec.Code != 400 && rec.Code != 413 {
		t.Fatalf("oversized: %d", rec.Code)
	}
	pr.allow.Store(0)
	for i := 1; i <= 10; i++ {
		if rec = pr.req("POST", "/auth/pair", form); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	rec = pr.req("POST", "/auth/pair", form)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "42" {
		t.Fatalf("11th POST: %d %v", rec.Code, rec.Header())
	}
	checkPageHeaders(t, rec)
}

func TestPairing_NoOptionalHooks(t *testing.T) {
	t.Parallel()
	ds, _ := newDevices(t)
	h := PairingHandler(PairingOptions{Cfg: config.Config{AuthMode: "proxy", PublicURL: "http://x"}, Devices: ds, FailDelay: time.Millisecond})
	code, _, _ := ds.CreatePairingCode(context.Background(), human, "P")
	r := httptest.NewRequest("POST", "/auth/pair", strings.NewReader(url.Values{"code": {code}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == DeviceCookie && c.Secure {
			t.Fatal("cookie Secure over http")
		}
	}
	// disabled store: pairing answers 404, never panics
	h = PairingHandler(PairingOptions{Cfg: config.Config{AuthMode: "proxy"}})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/pair", nil))
	if rec.Code != 404 {
		t.Fatalf("nil devices: %d", rec.Code)
	}
}

func TestStart_Chooser(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"proxy", "local"} {
		t.Run(mode, func(t *testing.T) {
			pr := newPairRig(t, mode, fakeSessions{})
			ctx := context.Background()
			sso := "/"
			if mode == "local" {
				sso = "/auth/login"
			}
			// none: chooser
			rec := pr.req("GET", "/start", "")
			body := rec.Body.String()
			if rec.Code != 200 || !strings.Contains(body, `action="/start"`) || !strings.Contains(body, `href="/pair"`) ||
				!strings.Contains(body, "Continue with sign-in") || !strings.Contains(body, "Pair this device") {
				t.Fatalf("chooser: %d %s", rec.Code, body)
			}
			checkPageHeaders(t, rec)
			if strings.Contains(body, "session ended") {
				t.Fatal("expired notice without ?expired=1")
			}
			// valid device cookie: 302 /
			code, _, _ := pr.ds.CreatePairingCode(ctx, human, "Phone")
			ck, _, _ := pr.ds.Redeem(ctx, code, "ua")
			if rec = pr.req("GET", "/start", "", "Cookie", DeviceCookie+"="+ck); rec.Code != 302 || rec.Header().Get("Location") != "/" {
				t.Fatalf("device: %d %q", rec.Code, rec.Header().Get("Location"))
			}
			// invalid device cookie: chooser
			if rec = pr.req("GET", "/start", "", "Cookie", DeviceCookie+"=garbage"); rec.Code != 200 {
				t.Fatalf("garbage device: %d", rec.Code)
			}
			// mm_start=sso
			if rec = pr.req("GET", "/start", "", "Cookie", "mm_start=sso"); rec.Code != 302 || rec.Header().Get("Location") != sso {
				t.Fatalf("mm_start: %d %q", rec.Code, rec.Header().Get("Location"))
			}
			// valid local session
			rec = pr.req("GET", "/start", "", "Cookie", "__Host-mm_session=goodsess")
			if mode == "local" {
				if rec.Code != 302 || rec.Header().Get("Location") != "/" {
					t.Fatalf("session: %d", rec.Code)
				}
			} else if rec.Code != 200 {
				t.Fatalf("proxy ignores session cookies: %d", rec.Code)
			}
			// POST /start sets mm_start
			rec = pr.req("POST", "/start", "")
			if rec.Code != 303 || rec.Header().Get("Location") != sso {
				t.Fatalf("POST /start: %d %q", rec.Code, rec.Header().Get("Location"))
			}
			var ms *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == "mm_start" {
					ms = c
				}
			}
			if ms == nil || ms.Value != "sso" || ms.MaxAge != 400*24*3600 || ms.SameSite != http.SameSiteLaxMode || !ms.Secure || ms.Path != "/" {
				t.Fatalf("mm_start cookie: %+v", ms)
			}
			// cross-site POST refused
			if rec = pr.req("POST", "/start", "", "Sec-Fetch-Site", "cross-site"); rec.Code != 403 || !strings.Contains(rec.Body.String(), "CROSS_ORIGIN") {
				t.Fatalf("cross-site POST: %d", rec.Code)
			}
			// ?expired=1 clears mm_start and says so; no redirect even with the cookie
			rec = pr.req("GET", "/start?expired=1", "", "Cookie", "mm_start=sso")
			cleared := false
			for _, c := range rec.Result().Cookies() {
				if c.Name == "mm_start" && c.MaxAge < 0 {
					cleared = true
				}
			}
			if rec.Code != 200 || !cleared || !strings.Contains(rec.Body.String(), "session ended") {
				t.Fatalf("expired: %d cleared=%v %s", rec.Code, cleared, rec.Body)
			}
		})
	}
}
