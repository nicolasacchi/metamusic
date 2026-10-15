package subsonictest

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Expect is the "expect" object of a fixture step; absent fields are not checked.
type Expect struct {
	HTTP        int               `json:"http"`
	Status      string            `json:"status"`
	Code        *int              `json:"code"`
	ContentType string            `json:"content_type"` // prefix of the Content-Type header
	Counts      map[string]int    `json:"counts"`       // dotted path into the parsed envelope => number of elements
	Headers     map[string]string `json:"headers"`      // exact header values (placeholders expanded)
	// BytesEqualMasterFrom compares the body with the master of the step's id parameter from that byte offset on
	// (a placeholder, e.g. "{{mid}}"); it needs "data_dir" in the PlaceholderSet.
	BytesEqualMasterFrom string `json:"bytes_equal_master_from"`
}

// Step is one line of a fixture file (C117.5).
type Step struct {
	Step string `json:"step"`
	// Skip, when set, disables the step with that reason (logged): the endpoint it exercises is not built yet.
	Skip    string              `json:"skip"`
	Advance string              `json:"advance"` // Go duration: advance the rig clock before the step
	Salt    string              `json:"salt"`    // label of the step whose salt is reused
	Method  string              `json:"method"`  // GET (default) or POST
	Path    string              `json:"path"`    // /rest/<endpoint>[.view]
	Params  map[string][]string `json:"params"`
	Auth    string              `json:"auth"` // token (default) | apikey | none
	Headers map[string]string   `json:"headers"`
	Expect  Expect              `json:"expect"`
	// Save stores values of the answer for later steps: placeholder name => dotted path (a scalar of the parsed
	// envelope, e.g. {"playlist": "playlist.id"}); later steps use {{playlist}}.
	Save map[string]string `json:"save"`
}

// saltFor is the deterministic salt of a step label, so a later step can reuse it by label.
func saltFor(label string) string {
	sum := sha256.Sum256([]byte("salt:" + label))
	return hex.EncodeToString(sum[:8])
}

// Replay runs one client fixture file (internal/subsonic/testdata/clients/*.jsonl): one subtest-free pass over its
// steps, failing the test at the first deviation with the step label. A format of f=json is chosen by the step's own
// params (the Amperfy file sends none and gets XML).
func Replay(tb testing.TB, c *Client, lib PlaceholderSet, path string) {
	tb.Helper()
	f, err := os.Open(path) //nolint:gosec // fixture path chosen by the test
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 || raw[0] == '#' {
			continue
		}
		var st Step
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&st); err != nil {
			tb.Fatalf("%s:%d: %v", filepath.Base(path), line, err)
		}
		if st.Step == "" {
			st.Step = fmt.Sprintf("line-%d", line)
		}
		runStep(tb, c, lib, &st, fmt.Sprintf("%s:%d (%s)", filepath.Base(path), line, st.Step))
	}
	if err := sc.Err(); err != nil {
		tb.Fatal(err)
	}
}

func runStep(tb testing.TB, c *Client, lib PlaceholderSet, st *Step, where string) {
	tb.Helper()
	fail := func(format string, a ...any) {
		tb.Helper()
		tb.Fatalf("%s: %s", where, fmt.Sprintf(format, a...))
	}
	if st.Skip != "" {
		tb.Logf("%s: skipped: %s", where, st.Skip)
		return
	}
	if st.Advance != "" {
		d, err := time.ParseDuration(st.Advance)
		if err != nil {
			fail("advance: %v", err)
		}
		if c.Clock == nil {
			fail("the step advances the clock but Client.Clock is nil")
		}
		c.Clock.Advance(d)
	}
	params := url.Values{}
	for k, vs := range st.Params {
		for _, v := range vs {
			x, err := lib.expand(v)
			if err != nil {
				fail("param %s: %v", k, err)
			}
			params.Add(k, x)
		}
	}
	endpoint := strings.TrimPrefix(st.Path, "/rest/")
	view := strings.HasSuffix(endpoint, ".view")
	endpoint = strings.TrimSuffix(endpoint, ".view")
	if endpoint == "" || endpoint == st.Path {
		fail("path %q is not /rest/<endpoint>", st.Path)
	}
	o := CallOpts{View: view, POST: strings.EqualFold(st.Method, "POST")}
	cl := *c
	switch st.Auth {
	case "", "token":
		label := st.Step
		if st.Salt != "" {
			label = st.Salt
		}
		o.Salt = saltFor(label)
		if cl.Password == "" {
			fail("token auth needs a client with a password")
		}
	case "apikey":
		o.APIKey = true
		if cl.APIKey == "" {
			fail("apikey auth needs a client with an API key")
		}
	case "none":
		cl.Password, cl.APIKey = "", ""
	default:
		fail("unknown auth %q", st.Auth)
	}
	for k, v := range st.Headers {
		x, err := lib.expand(v)
		if err != nil {
			fail("header %s: %v", k, err)
		}
		switch strings.ToLower(k) {
		case "range":
			o.Range = x
		case "user-agent":
			o.UserAgent = x
		default:
			fail("unsupported request header %q", k)
		}
	}
	if o.JSON = params.Get("f") == "json"; o.JSON {
		params.Del("f")
	}
	resp := cl.Call(tb, endpoint, params, o)
	check(tb, resp, lib, st, params, where)
	for name, path := range st.Save {
		v, ok := ScalarAt(resp.Doc, path)
		if !ok {
			tb.Fatalf("%s: save %s: no scalar at %q (%.300s)", where, name, path, resp.Body)
		}
		lib[name] = v
	}
}

// ScalarAt returns the scalar at the dotted path of a parsed envelope as a string (numbers without exponent).
func ScalarAt(doc map[string]any, path string) (string, bool) {
	var cur any = doc
	for _, seg := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return "", false
			}
			cur = v[i]
		default:
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}

func check(tb testing.TB, r *Response, lib PlaceholderSet, st *Step, params url.Values, where string) {
	tb.Helper()
	e := st.Expect
	fail := func(format string, a ...any) {
		tb.Helper()
		tb.Fatalf("%s: %s", where, fmt.Sprintf(format, a...))
	}
	if e.HTTP != 0 && r.HTTP != e.HTTP {
		fail("http %d, want %d (%.200s)", r.HTTP, e.HTTP, r.Body)
	}
	if e.Status != "" && r.Status != e.Status {
		fail("status %q, want %q (%.200s)", r.Status, e.Status, r.Body)
	}
	if e.Code != nil && r.Code != *e.Code {
		fail("error code %d, want %d (%.200s)", r.Code, *e.Code, r.Body)
	}
	if e.ContentType != "" && !strings.HasPrefix(r.Header.Get("Content-Type"), e.ContentType) {
		fail("content type %q, want prefix %q", r.Header.Get("Content-Type"), e.ContentType)
	}
	for k, v := range e.Headers {
		want, err := lib.expand(v)
		if err != nil {
			fail("expected header %s: %v", k, err)
		}
		if got := r.Header.Get(k); got != want {
			fail("header %s = %q, want %q", k, got, want)
		}
	}
	for path, want := range e.Counts {
		if got := CountAt(r.Doc, path); got != want {
			fail("count at %q = %d, want %d (%.300s)", path, got, want, r.Body)
		}
	}
	if e.BytesEqualMasterFrom != "" {
		from, err := lib.expand(e.BytesEqualMasterFrom)
		if err != nil {
			fail("bytes_equal_master_from: %v", err)
		}
		off, err := strconv.ParseInt(from, 10, 64)
		if err != nil || off < 0 {
			fail("bytes_equal_master_from %q is not an offset", from)
		}
		rel := lib["master:"+params.Get("id")]
		if rel == "" || lib["data_dir"] == "" {
			fail("bytes_equal_master_from needs the id parameter of a song and data_dir in the placeholder set")
		}
		master, err := os.ReadFile(filepath.Join(lib["data_dir"], filepath.FromSlash(rel))) //nolint:gosec // rig data directory
		if err != nil {
			fail("reading the master: %v", err)
		}
		if off > int64(len(master)) || !bytes.Equal(r.Body, master[off:]) {
			fail("body (%d bytes) differs from the master from offset %d (%d bytes)", len(r.Body), off, len(master))
		}
	}
}

// CountAt returns the number of elements at the dotted path of a parsed envelope: 0 when absent, the length of a
// list, 1 for a single object or scalar. A numeric segment indexes a list.
func CountAt(doc map[string]any, path string) int {
	var cur any = doc
	for _, seg := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return 0
			}
			cur = v[i]
		default:
			return 0
		}
		if cur == nil {
			return 0
		}
	}
	if l, ok := cur.([]any); ok {
		return len(l)
	}
	return 1
}
