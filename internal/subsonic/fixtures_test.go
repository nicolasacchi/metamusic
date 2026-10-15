package subsonic_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	placeholderRE = regexp.MustCompile(`\{\{[a-z_]+(:[0-9]+)?\}\}`)
	uuidRE        = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// plainRE is what remains of a value once placeholders are removed: a short protocol token or a number
	// (no spaces, so no sentences), e.g. newest, raw, true, 1.13.0, bytes=0-1, ar-metamusic, "".
	plainRE = regexp.MustCompile(`^[A-Za-z0-9._\-="/,]{0,24}$`)
	// credential parameters may only appear in a fixture as a placeholder (and none is defined): the harness signs.
	credParams = map[string]bool{"u": true, "p": true, "t": true, "s": true, "apiKey": true}
	// request headers may carry a byte range or a synthetic client name only
	rangeRE = regexp.MustCompile(`^bytes=(\d+|\{\{mid\}\})-(\d+)?$`)
)

type fixtureStep struct {
	Step    string              `json:"step"`
	Skip    string              `json:"skip"`
	Advance string              `json:"advance"`
	Salt    string              `json:"salt"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Params  map[string][]string `json:"params"`
	Auth    string              `json:"auth"`
	Headers map[string]string   `json:"headers"`
	Expect  map[string]any      `json:"expect"`
	Save    map[string]string   `json:"save"`
}

// TestSubsonicFixtures_Sanitised: the synthetic client files hold no credentials, no 36-character ids, no free text
// outside placeholders and no private-looking strings; every step has a unique label, a known auth form and
// expectations, and a reused salt names an earlier step.
func TestSubsonicFixtures_Sanitised(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("testdata", "clients", "*.jsonl"))
	if err != nil || len(files) < 3 {
		t.Fatalf("client fixtures: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			t.Parallel()
			fh, err := os.Open(f) //nolint:gosec // glob of the testdata directory
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fh.Close() }()
			sc := bufio.NewScanner(fh)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			labels := map[string]bool{}
			for n := 1; sc.Scan(); n++ {
				line := sc.Bytes()
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				var st fixtureStep
				dec := json.NewDecoder(bytes.NewReader(line))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&st); err != nil {
					t.Fatalf("line %d: %v", n, err)
				}
				at := func(what string) string { return filepath.Base(f) + ":" + st.Step + ": " + what }
				if st.Step == "" || labels[st.Step] {
					t.Errorf("line %d: missing or duplicate step label %q", n, st.Step)
				}
				if st.Salt != "" && !labels[st.Salt] {
					t.Errorf("%s", at("salt names a step that does not come earlier"))
				}
				labels[st.Step] = true
				if !strings.HasPrefix(st.Path, "/rest/") {
					t.Errorf("%s", at("path "+st.Path))
				}
				if m := strings.ToUpper(st.Method); m != "GET" && m != "POST" {
					t.Errorf("%s", at("method "+st.Method))
				}
				switch st.Auth {
				case "token", "apikey", "none":
				default:
					t.Errorf("%s", at("auth "+st.Auth))
				}
				if len(st.Expect) == 0 {
					t.Errorf("%s", at("no expectations"))
				}
				for k, vs := range st.Params {
					if credParams[k] {
						t.Errorf("%s", at("credential parameter "+k))
					}
					for _, v := range vs {
						checkValue(t, at("param "+k), v)
					}
				}
				for k, v := range st.Headers {
					if !strings.EqualFold(k, "range") || !rangeRE.MatchString(v) {
						t.Errorf("%s", at("header "+k+": "+v))
					}
				}
			}
			if err := sc.Err(); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(f) //nolint:gosec // see above
			for _, bad := range []string{"mms_", "mmp_", "http://", "https://", "@", "Bearer", "Authorization"} {
				if bytes.Contains(raw, []byte(bad)) {
					t.Errorf("%s contains %q", f, bad)
				}
			}
		})
	}
}

func checkValue(t *testing.T, where, v string) {
	t.Helper()
	if p := valueProblem(v); p != "" {
		t.Errorf("%s: %s: %q", where, p, v)
	}
}

// valueProblem names what is wrong with a fixture value, "" when it is fine.
func valueProblem(v string) string {
	if uuidRE.MatchString(v) {
		return "holds a 36-character id"
	}
	if rest := placeholderRE.ReplaceAllString(v, ""); !plainRE.MatchString(rest) {
		return "free text outside placeholders"
	}
	return ""
}

// TestSubsonicFixtures_SanitisedRejects: the checks themselves catch what they are for.
func TestSubsonicFixtures_SanitisedRejects(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"a free text sentence", "01900000-0000-7000-8000-000000000101", "Some Real Title Here Indeed"} {
		if valueProblem(bad) == "" {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, good := range []string{"{{song:1}}", "newest", "1.13.0", `""`, "{{mid}}-", "ar-metamusic", ""} {
		if p := valueProblem(good); p != "" {
			t.Errorf("rejected %q: %s", good, p)
		}
	}
}
