package subsonic_test

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

const xmlNamespace = "http://subsonic.org/restapi"

// TestSubsonic_XMLGolden: one golden per implemented envelope endpoint (the XML twin of the JSON sweep, rewritten with
// -update). Every answer parses with encoding/xml and its root is subsonic-response in the Subsonic namespace.
func TestSubsonic_XMLGolden(t *testing.T) {
	t.Parallel()
	_, c, ps := fixtureRig(t)
	dir := filepath.Join("testdata", "xml")
	seen := map[string]bool{}
	runSweep(t, c, ps, subsonictest.CallOpts{View: true}, func(name string, r *subsonictest.Response) {
		seen[name+".golden.xml"] = true
		dec := xml.NewDecoder(bytes.NewReader(r.Body))
		var root *xml.StartElement
		for root == nil {
			tok, err := dec.Token()
			if err != nil {
				t.Fatalf("%s: not XML: %v", name, err)
			}
			if se, ok := tok.(xml.StartElement); ok {
				root = &se
			}
		}
		if root.Name.Local != "subsonic-response" || root.Name.Space != xmlNamespace {
			t.Errorf("%s: root %q in namespace %q", name, root.Name.Local, root.Name.Space)
		}
		for {
			if _, err := dec.Token(); err != nil {
				if err.Error() != "EOF" {
					t.Errorf("%s: malformed XML: %v", name, err)
				}
				break
			}
		}
		path := filepath.Join(dir, name+".golden.xml")
		if updateGoldens() {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, r.Body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path) //nolint:gosec // golden path built from the endpoint list
		if err != nil {
			t.Errorf("%s: %v (run with -update)", name, err)
			return
		}
		if !bytes.Equal(want, r.Body) {
			t.Errorf("%s differs from %s:\n got: %s\nwant: %s", name, path, r.Body, want)
		}
	})
	// no stale golden for an endpoint that left the sweep
	entries, err := os.ReadDir(dir)
	if err != nil && !updateGoldens() {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !seen[e.Name()] {
			t.Errorf("stale golden %s", e.Name())
		}
	}
}
