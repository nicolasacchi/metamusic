package subsonic

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubsonic_Routing(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	q := func() url.Values { return keyParams(r.rwKey) }

	a := r.get("ping", q(), "198.51.100.1")
	b := r.get("ping.view", q(), "198.51.100.1")
	if a.Code != 200 || a.code(t) != 0 || b.Code != 200 || b.code(t) != 0 {
		t.Fatalf("ping %d %s / ping.view %d %s", a.Code, a.Body, b.Code, b.Body)
	}
	for _, p := range []string{"Ping", "ping/x", "pi%20ng", "ping.xml", ""} {
		w := r.get(p, q(), "198.51.100.1")
		if w.Code != 200 || w.code(t) != 0 || !strings.Contains(w.Body.String(), "unsupported endpoint") {
			t.Errorf("/rest/%s: %d %s, want error 0 unsupported endpoint", p, w.Code, w.Body)
		}
	}

	// PUT and DELETE: 405 with Allow, an error envelope
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := r.do(httptest.NewRequest(m, "/rest/ping?"+q().Encode(), nil))
		if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD, POST" || w.code(t) != 0 {
			t.Errorf("%s: %d allow=%q %s", m, w.Code, w.Header().Get("Allow"), w.Body)
		}
	}

	// form POST works; credentials may travel in the body
	form := keyParams(r.rwKey).Encode()
	req := httptest.NewRequest(http.MethodPost, "/rest/ping", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if w := r.do(req); w.Code != 200 || w.code(t) != 0 {
		t.Errorf("form POST: %d %s", w.Code, w.Body)
	}
	// a JSON body is error 0
	req = httptest.NewRequest(http.MethodPost, "/rest/ping?"+q().Encode(), strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	if w := r.do(req); w.Code != 200 || w.code(t) != 0 || !strings.Contains(w.Body.String(), "unsupported content type") {
		t.Errorf("JSON POST: %d %s", w.Code, w.Body)
	}
	// a form larger than 64 KiB is error 0
	big := keyParams(r.rwKey).Encode() + "&x=" + strings.Repeat("a", 64<<10)
	req = httptest.NewRequest(http.MethodPost, "/rest/ping?f=json", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if w := r.do(req); w.Code != 200 || w.code(t) != 0 || !strings.Contains(w.Body.String(), "too large") {
		t.Errorf("big form: %d %s", w.Code, w.Body)
	}
	// a form of exactly 64 KiB is accepted
	exact := keyParams(r.rwKey).Encode() + "&x="
	exact += strings.Repeat("a", 64<<10-len(exact))
	req = httptest.NewRequest(http.MethodPost, "/rest/ping", strings.NewReader(exact))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if w := r.do(req); w.Code != 200 || w.Body.Len() == 0 || w.code(t) != 0 {
		t.Errorf("64 KiB form: %d %s", w.Code, w.Body)
	}

	// HEAD: headers only
	req = httptest.NewRequest(http.MethodHead, "/rest/ping?"+q().Encode(), nil)
	w := r.do(req)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("HEAD: %d %v", w.Code, w.Header())
	}
}

// TestSubsonic_HeadOverHTTP: over a real connection a HEAD answer carries no body.
func TestSubsonic_HeadOverHTTP(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	ts := httptest.NewServer(r.srv)
	t.Cleanup(ts.Close)
	resp, err := http.Head(ts.URL + "/rest/ping?" + keyParams(r.rwKey).Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 8)
	if n, _ := resp.Body.Read(buf); n != 0 || resp.StatusCode != 200 {
		t.Errorf("HEAD: status %d, %d body bytes", resp.StatusCode, n)
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // test data path
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs:\n got: %s\nwant: %s", name, got, want)
	}
}

func TestSubsonic_Envelopes(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	okJSON := keyParams(r.rwKey)
	okXML := keyParams(r.rwKey)
	okXML.Del("f")
	badJSON := keyParams("mms_" + strings.Repeat("a", 52))
	badXML := keyParams("mms_" + strings.Repeat("a", 52))
	badXML.Del("f")

	cases := []struct {
		golden string
		q      url.Values
		ct     string
	}{
		{"ping_ok.json", okJSON, "application/json"},
		{"ping_ok.xml", okXML, "text/xml; charset=utf-8"},
		{"error_44.json", badJSON, "application/json"},
		{"error_44.xml", badXML, "text/xml; charset=utf-8"},
	}
	for _, c := range cases {
		w := r.get("ping", c.q, "198.51.100."+string(rune('1'+len(c.golden)%5)))
		if w.Code != 200 || w.Header().Get("Content-Type") != c.ct {
			t.Errorf("%s: %d %q", c.golden, w.Code, w.Header().Get("Content-Type"))
		}
		for h, want := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
			if w.Header().Get(h) != want {
				t.Errorf("%s: %s = %q", c.golden, h, w.Header().Get(h))
			}
		}
		golden(t, c.golden, w.Body.Bytes())
	}

	// the XML envelope parses, in the OpenSubsonic namespace, with openSubsonic true
	var x struct {
		XMLName      xml.Name `xml:"subsonic-response"`
		Status       string   `xml:"status,attr"`
		Version      string   `xml:"version,attr"`
		OpenSubsonic string   `xml:"openSubsonic,attr"`
		Type         string   `xml:"type,attr"`
	}
	w := r.get("ping", okXML, "198.51.100.1")
	if err := xml.Unmarshal(w.Body.Bytes(), &x); err != nil || x.XMLName.Space != "http://subsonic.org/restapi" ||
		x.Status != "ok" || x.Version != "1.16.1" || x.OpenSubsonic != "true" || x.Type != "metamusic" {
		t.Errorf("xml envelope %+v (%v)", x, err)
	}
	if !strings.HasPrefix(w.Body.String(), `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Errorf("xml header: %.60s", w.Body)
	}

	// jsonp (and any unknown format) answers a JSON error 0
	for _, f := range []string{"jsonp", "yaml"} {
		q := keyParams(r.rwKey)
		q.Set("f", f)
		q.Set("callback", "cb")
		w := r.get("ping", q, "198.51.100.1")
		if w.Header().Get("Content-Type") != "application/json" || w.code(t) != 0 || !strings.Contains(w.Body.String(), "format not supported") ||
			strings.HasPrefix(w.Body.String(), "cb(") {
			t.Errorf("f=%s: %d %q %s", f, w.Code, w.Header().Get("Content-Type"), w.Body)
		}
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(r.get("ping", okJSON, "198.51.100.1").Body.Bytes(), &env); err != nil || len(env) != 1 {
		t.Errorf("json envelope root: %v %v", env, err)
	}
}

func TestSubsonic_CommonParams(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	del := func(k string) url.Values {
		q := keyParams(r.rwKey)
		q.Del(k)
		return q
	}
	set := func(k, v string) url.Values {
		q := keyParams(r.rwKey)
		q.Set(k, v)
		return q
	}
	for name, tc := range map[string]struct {
		q    url.Values
		code int
	}{
		"missing c":     {del("c"), 10},
		"missing v":     {del("v"), 10},
		"v=2.0.0":       {set("v", "2.0.0"), 30},
		"v=1.13.0":      {set("v", "1.13.0"), 0},
		"v=1.16":        {set("v", "1.16"), 0},
		"v=0.9.0":       {set("v", "0.9.0"), 0},
		"v garbage":     {set("v", "latest"), 10},
		"v=1.2.3.4":     {set("v", "1.2.3.4"), 10},
		"empty c":       {set("c", ""), 10},
		"v=100.0":       {set("v", "100.0"), 30},
		"v=1.16.1":      {set("v", "1.16.1"), 0},
		"v=1.99.99":     {set("v", "1.99.99"), 0},
		"v=2":           {set("v", "2"), 10},
		"v=1.0.0-extra": {set("v", "1.0.0-extra"), 10},
	} {
		if got := r.get("ping", tc.q, "198.51.100.1").code(t); got != tc.code {
			t.Errorf("%s: error %d, want %d", name, got, tc.code)
		}
	}
}

// TestSubsonic_RepeatedParams: query values come first, then form values, in order (repeatable parameters such as
// id and songId keep both); a single-valued getter takes the first.
func TestSubsonic_RepeatedParams(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	var got []string
	var first string
	r.srv.add("echoTest", false, func(c *call) error {
		got, first = c.all("id"), c.str("id")
		return nil
	})
	q := keyParams(r.rwKey)
	q.Add("id", "q1")
	q.Add("id", "q2")
	form := keyParams(r.rwKey)
	form.Add("id", "f1")
	req := httptest.NewRequest(http.MethodPost, "/rest/echoTest?"+q.Encode(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if w := r.do(req); w.code(t) != 0 {
		t.Fatal(w.Body)
	}
	if strings.Join(got, ",") != "q1,q2,f1" || first != "q1" {
		t.Errorf("id values %v first %q", got, first)
	}
}
