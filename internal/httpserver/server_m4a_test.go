package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// With MM_OIDC_ISSUER unset the server builds auth.Verifiers{Connectors: nil}: a JWT-shaped bearer is just an
// unknown bearer (bare challenge), never a nil-pointer panic, and /sw.js needs no credentials.
func TestHTTPServer_NilConnectorsNoPanic(t *testing.T) {
	t.Parallel()
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("mcp reached with a JWT") })
	e := newEnv(t, nil, spaFS, mcp)
	for _, path := range []string{"/mcp", "/mcp/x", "/api/tracks", "/rest/ping"} {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln")
		w := e.do(r)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: %d %q", path, w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
	if w := e.do(httptest.NewRequest("GET", "/sw.js", nil)); w.Code == 401 || w.Code == 302 || w.Code == 303 {
		t.Errorf("/sw.js must be public: %d", w.Code)
	}
}
