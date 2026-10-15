package subsonic_test

import (
	"encoding/json"
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/subsonic"
)

// TestStubHandler_Error0: until M4a-S1 every endpoint answers protocol error 0 in the requested format.
func TestStubHandler_Error0(t *testing.T) {
	t.Parallel()
	h := subsonic.NewHandler(subsonic.Options{Version: "test"})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/rest/ping.view?v=1.16.1&c=x", nil))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/xml") {
		t.Fatalf("xml: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var x struct {
		XMLName xml.Name `xml:"subsonic-response"`
		Status  string   `xml:"status,attr"`
		Error   struct {
			Code int `xml:"code,attr"`
		} `xml:"error"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &x); err != nil || x.XMLName.Space != "http://subsonic.org/restapi" || x.Status != "failed" || x.Error.Code != 0 {
		t.Fatalf("xml body %s (%v)", w.Body, err)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/rest/getSong?f=json", nil))
	var j map[string]struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Error   struct{ Code int }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil || j["subsonic-response"].Status != "failed" || j["subsonic-response"].Version != subsonic.APIVersion {
		t.Fatalf("json body %s (%v)", w.Body, err)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", w.Header())
	}
}
