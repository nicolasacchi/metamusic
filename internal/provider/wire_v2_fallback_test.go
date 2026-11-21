package provider

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fallbackSpec is a plain /2 render whose every upload also names a fallback destination (BULK-R2, C399.4).
func fallbackSpec() TakeSpec {
	t := specV2(KindRender, nil, nil)
	t.Kind = "" // a plain render: it is /2 only because the attempt has a fallback (WireFor)
	for n, u := range t.Uploads {
		u.Fallback = &FallbackTarget{URL: "https://bucket.example.com/mm/" + strings.ReplaceAll(n, ".", "-") + "?X-Amz-Signature=abc"}
		t.Uploads[n] = u
	}
	t.FallbackAfterS = 120
	return t
}

// TestWorkerInputV2_FallbackGolden: the /2 builder emits uploads[name].fallback.url and the top-level fallback_after_s, the parser
// accepts the document, and a /1 document of the same spec carries neither.
func TestWorkerInputV2_FallbackGolden(t *testing.T) {
	t.Parallel()
	spec := fallbackSpec()
	if got := WireFor(spec, nil, true); got != ContractV2 {
		t.Fatalf("a plain job with a fallback goes out as %s", got)
	}
	raw, err := BuildWorkerInput(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseWorkerInput(raw); err != nil {
		t.Fatalf("the parser refuses the built document: %v", err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	golden := filepath.Join("testdata", "worker-input-v2-render-fallback.json")
	if *updateE2 {
		if err := os.WriteFile(golden, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, pretty.Bytes()) {
		t.Errorf("the /2 fallback document changed\n got %s\nwant %s", pretty.Bytes(), want)
	}
	v1 := spec
	v1.Wire = ContractV1
	v1.FallbackAfterS = 0
	rawV1, err := BuildWorkerInput(v1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawV1, []byte("fallback")) || bytes.Contains(rawV1, []byte("X-Amz")) {
		t.Error("a /1 document carries fallback fields")
	}
}
