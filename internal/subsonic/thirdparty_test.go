package subsonic_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const thirdPartyDir = "../../third_party/opensubsonic"

// TestThirdParty_OpenSubsonicPinned: the sha256 of the vendored description equals the value in NOTICE.md, the
// license is Apache-2.0, the document is self-contained and the root notices list it.
func TestThirdParty_OpenSubsonicPinned(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(thirdPartyDir, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	notice, err := os.ReadFile(filepath.Join(thirdPartyDir, "NOTICE.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("sha256 of the vendored description: `([0-9a-f]{64})`").FindSubmatch(notice)
	if m == nil || string(m[1]) != hex.EncodeToString(sum[:]) {
		t.Fatalf("NOTICE.md sha256 %q does not match the file %s", m, hex.EncodeToString(sum[:]))
	}
	lic, err := os.ReadFile(filepath.Join(thirdPartyDir, "LICENSE"))
	if err != nil || !strings.Contains(string(lic), "Apache License") || !strings.Contains(string(lic), "Version 2.0") {
		t.Fatalf("LICENSE is not the Apache-2.0 text (%v)", err)
	}
	var doc struct {
		OpenAPI    string                           `json:"openapi"`
		Paths      map[string]any                   `json:"paths"`
		Components struct{ Schemas map[string]any } `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.OpenAPI != "3.0.0" || len(doc.Paths) < 80 || len(doc.Components.Schemas) < 100 {
		t.Fatalf("description looks wrong: %q paths=%d schemas=%d (%v)", doc.OpenAPI, len(doc.Paths), len(doc.Components.Schemas), err)
	}
	if regexp.MustCompile(`"\$ref":\s*"\.`).Match(raw) {
		t.Error("the bundled description still has relative file references")
	}
	notices, err := os.ReadFile("../../THIRD_PARTY_NOTICES.md")
	if err != nil || !strings.Contains(string(notices), "third_party/opensubsonic/openapi.json") {
		t.Errorf("THIRD_PARTY_NOTICES.md does not list the description (%v)", err)
	}
}

// TestSubsonic_AttributionHeader: types.go starts with the binding attribution text (C108.5).
func TestSubsonic_AttributionHeader(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatal(err)
	}
	head := string(src)
	if i := strings.Index(head, "\npackage "); i >= 0 {
		head = head[:i]
	}
	head = strings.Join(strings.Fields(strings.ReplaceAll(head, "//", " ")), " ")
	for _, want := range []string{
		"Field names and semantics follow the OpenSubsonic API description",
		"third_party/opensubsonic/openapi.json, commit be131fb44a48fb346faa16f5a242785fdfe3052b, Apache-2.0,",
		"© OpenSubsonic contributors",
		"Written for this project from that description; no code from GPL-licensed Subsonic servers or client libraries.",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("types.go header lacks %q", want)
		}
	}
}
