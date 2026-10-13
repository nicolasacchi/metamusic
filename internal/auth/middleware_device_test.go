package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deviceFixture struct {
	ds                        *DeviceStore
	clk                       *fakeClock
	valid, revoked, expiredCk string
}

func newDeviceFixture(t *testing.T) *deviceFixture {
	t.Helper()
	ctx := context.Background()
	ds, clk := newDevices(t)
	mk := func(name string) (string, DeviceInfo) {
		code, _, err := ds.CreatePairingCode(ctx, human, name)
		if err != nil {
			t.Fatal(err)
		}
		ck, dev, err := ds.Redeem(ctx, code, "ua")
		if err != nil {
			t.Fatal(err)
		}
		return ck, dev
	}
	f := &deviceFixture{ds: ds, clk: clk}
	f.valid, _ = mk("valid")
	var rev DeviceInfo
	f.revoked, rev = mk("revoked")
	if _, err := ds.Revoke(ctx, rev.ID); err != nil {
		t.Fatal(err)
	}
	// "expired": expiry in the past, written directly
	var exp DeviceInfo
	f.expiredCk, exp = mk("expired")
	if _, err := ds.st.W.Exec(`UPDATE devices SET expires_at=? WHERE id=?`, clk.Now().Add(-time.Hour).UTC().Format(time.RFC3339), exp.ID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *deviceFixture) cookie(state string) string {
	switch state {
	case "valid":
		return f.valid
	case "revoked":
		return f.revoked
	case "expired":
		return f.expiredCk
	case "garbage":
		return "garbage"
	}
	return ""
}

func (f *deviceFixture) serve(mode string, r *http.Request) (*httptest.ResponseRecorder, Kind) {
	c, s := cfg(mode)
	var seen Kind
	h := Middleware(c, s, fakeTokens{}, fakeSessions{}, f.ds)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context()).Kind
		w.WriteHeader(200)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, seen
}

func clearsDevice(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == DeviceCookie && c.MaxAge < 0 && c.Value == "" {
			return true
		}
	}
	return false
}

func setsDevice(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == DeviceCookie && c.Value != "" {
			return true
		}
	}
	return false
}

func TestTrustMatrix_DeviceCookie(t *testing.T) {
	t.Parallel()
	f := newDeviceFixture(t)
	for _, path := range []string{"/", "/api/tracks", "/human/tokens", "/human/api/tokens", "/mcp", "/media/x", "/start"} {
		for _, state := range []string{"absent", "valid", "revoked", "expired", "garbage"} {
			for _, mode := range []string{"proxy", "local"} {
				for _, spoof := range []bool{false, true} {
					name := fmt.Sprintf("%s|%s|%s|spoof=%v", path, state, mode, spoof)
					t.Run(name, func(t *testing.T) {
						r := httptest.NewRequest("GET", path, nil)
						r.RemoteAddr = "192.0.2.1:5555" // untrusted peer: spoofed headers never count
						if spoof {
							r.Header.Set("Remote-User", "alice")
							r.Header.Set("X-MM-Edge", edge)
						}
						if ck := f.cookie(state); ck != "" {
							r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: ck})
						}
						rec, seen := f.serve(mode, r)

						class := Classify(path)
						underHuman := strings.HasPrefix(path, "/human/")
						isAPI := strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/human/api/")
						switch {
						case class != ClassUI:
							if seen != KindAnonymous || rec.Code != 200 || clearsDevice(rec) || setsDevice(rec) {
								t.Fatalf("machine/public path: seen %q code %d, cookie touched", seen, rec.Code)
							}
						case !underHuman && state == "valid":
							if seen != KindDevice || rec.Code != 200 {
								t.Fatalf("seen %q code %d", seen, rec.Code)
							}
							if clearsDevice(rec) {
								t.Fatal("valid cookie cleared")
							}
						case !underHuman && state != "absent":
							// read and rejected: cleared; HTML bounces to /start, API stays 401
							if seen != "" || !clearsDevice(rec) {
								t.Fatalf("rejected cookie: seen %q cleared %v", seen, clearsDevice(rec))
							}
							if isAPI {
								if rec.Code != 401 || !strings.Contains(rec.Body.String(), "UNAUTHENTICATED") {
									t.Fatalf("API: %d %s", rec.Code, rec.Body)
								}
							} else if rec.Code != 303 || rec.Header().Get("Location") != "/start?expired=1" {
								t.Fatalf("HTML: %d %q", rec.Code, rec.Header().Get("Location"))
							}
						default:
							// no cookie, or under /human/ where the cookie is never read
							if seen != "" || clearsDevice(rec) || setsDevice(rec) {
								t.Fatalf("seen %q, cookie touched", seen)
							}
							switch {
							case isAPI:
								if rec.Code != 401 {
									t.Fatalf("code %d", rec.Code)
								}
							case mode == "local":
								if rec.Code != 303 || rec.Header().Get("Location") != "/auth/login" {
									t.Fatalf("local HTML: %d %q", rec.Code, rec.Header().Get("Location"))
								}
							default:
								if rec.Code != 401 {
									t.Fatalf("proxy HTML: %d", rec.Code)
								}
							}
						}
					})
				}
			}
		}
	}
}

// A valid device cookie under /human/ is ignored and never cleared; it keeps
// authenticating "/" afterwards.
func TestDeviceCookie_HumanPrefixLeavesCookieAlone(t *testing.T) {
	t.Parallel()
	f := newDeviceFixture(t)
	for _, mode := range []string{"proxy", "local"} {
		for _, path := range []string{"/human/tokens", "/human/api/tokens", "/human/"} {
			r := httptest.NewRequest("GET", path, nil)
			r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: f.valid})
			rec, seen := f.serve(mode, r)
			if seen != "" || clearsDevice(rec) || strings.Contains(strings.Join(rec.Header().Values("Set-Cookie"), ";"), DeviceCookie+"=") {
				t.Fatalf("%s %s: seen %q headers %v", mode, path, seen, rec.Header())
			}
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: f.valid})
		if rec, seen := f.serve(mode, r); seen != KindDevice || rec.Code != 200 {
			t.Fatalf("%s: cookie no longer works: %q %d", mode, seen, rec.Code)
		}
	}
}

func TestDeviceCookie_ReissuedOnlyWhenExpirySlid(t *testing.T) {
	t.Parallel()
	f := newDeviceFixture(t)
	get := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/tracks", nil)
		r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: f.valid})
		rec, seen := f.serve("proxy", r)
		if seen != KindDevice {
			t.Fatalf("seen %q", seen)
		}
		return rec
	}
	if rec := get(); setsDevice(rec) {
		t.Fatal("re-issued on the first request")
	}
	f.clk.Advance(13 * time.Hour)
	rec := get()
	var got *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == DeviceCookie {
			got = c
		}
	}
	if got == nil || got.Value != f.valid || !got.HttpOnly || !got.Secure || got.SameSite != http.SameSiteLaxMode ||
		got.Path != "/" || got.MaxAge != int(DeviceTTL.Seconds()) {
		t.Fatalf("cookie after the gap: %+v", got)
	}
	f.clk.Advance(time.Minute)
	if setsDevice(get()) {
		t.Fatal("re-issued a minute later")
	}
}

func TestDeviceCookie_CrossOriginRefused(t *testing.T) {
	t.Parallel()
	f := newDeviceFixture(t)
	r := httptest.NewRequest("POST", "/api/tracks", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: f.valid})
	rec, seen := f.serve("proxy", r)
	if seen != "" || rec.Code != 403 || !strings.Contains(rec.Body.String(), "CROSS_ORIGIN") {
		t.Fatalf("%q %d %s", seen, rec.Code, rec.Body)
	}
}

func TestDeviceCookie_BearerWins(t *testing.T) {
	t.Parallel()
	f := newDeviceFixture(t)
	r := httptest.NewRequest("GET", "/api/tracks", nil)
	r.Header.Set("Authorization", "Bearer mmp_bad")
	r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: f.valid})
	rec, seen := f.serve("proxy", r)
	if seen != "" || rec.Code != 401 || clearsDevice(rec) {
		t.Fatalf("%q %d", seen, rec.Code)
	}
}

type failingDevices struct{}

func (failingDevices) VerifyDevice(context.Context, string) (Principal, error) {
	return Principal{}, fmt.Errorf("db down")
}

func TestDeviceCookie_InfrastructureErrorIs500(t *testing.T) {
	t.Parallel()
	c, s := cfg("proxy")
	h := Middleware(c, s, fakeTokens{}, fakeSessions{}, failingDevices{})(http.NotFoundHandler())
	r := httptest.NewRequest("GET", "/api/tracks", nil)
	r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: "x"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 500 || clearsDevice(rec) {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

// A plain DeviceVerifier (no slide information) still authenticates.
type plainDevices struct{}

func (plainDevices) VerifyDevice(_ context.Context, ck string) (Principal, error) {
	if ck == "ok" {
		return Principal{Kind: KindDevice, ID: "d1", Name: "d", Scopes: []Scope{ScopeRead, ScopeWrite}, Via: "device"}, nil
	}
	return Principal{}, ErrInvalidDevice
}

func TestDeviceCookie_PlainVerifier(t *testing.T) {
	t.Parallel()
	c, s := cfg("proxy")
	var seen Kind
	h := Middleware(c, s, fakeTokens{}, fakeSessions{}, plainDevices{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context()).Kind
	}))
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: "ok"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if seen != KindDevice || setsDevice(rec) {
		t.Fatalf("%q %v", seen, rec.Header())
	}
}
