package lineage

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

const fixedRequest = `{"cot":"full","id":"syn-a-s1101","lyrics":"[Verse]\nla la","seed":1101,"style":"pop, 100 BPM"}`

func TestRecipeKeys(t *testing.T) {
	t.Parallel()
	content, render := Keys([]byte(fixedRequest))
	// golden values computed independently (python3: sorted keys, compact separators, sha256) from the
	// request without id (render) and without id and seed (content)
	const wantRender = "9442b481196cbf8ad7c38733270a5bf019eea4c222e536711db2bfb6b853b1e5"
	const wantContent = "fb7f2bf48efe9057f9bfdf34581208142aa20815ded274ab3d4deb9c395a9df7"
	if render != wantRender || content != wantContent {
		t.Fatalf("golden: render %s content %s", render, content)
	}

	// render hash is equal across id changes, different across seed; content hash is equal across both
	idChanged := `{"cot":"full","id":"another-id","lyrics":"[Verse]\nla la","seed":1101,"style":"pop, 100 BPM"}`
	seedChanged := `{"cot":"full","id":"syn-a-s1101","lyrics":"[Verse]\nla la","seed":1102,"style":"pop, 100 BPM"}`
	c2, r2 := Keys([]byte(idChanged))
	c3, r3 := Keys([]byte(seedChanged))
	if r2 != render || c2 != content {
		t.Errorf("id change moved a key: %s %s", r2, c2)
	}
	if r3 == render || c3 != content {
		t.Errorf("seed change: render equal=%v content equal=%v", r3 == render, c3 == content)
	}
	if content == render {
		t.Error("content and render hashes must differ")
	}

	// anything else changes both
	for name, req := range map[string]string{
		"style":  `{"cot":"full","id":"x","lyrics":"[Verse]\nla la","seed":1101,"style":"pop, 90 BPM"}`,
		"lyrics": `{"cot":"full","id":"x","lyrics":"[Verse]\nla","seed":1101,"style":"pop, 100 BPM"}`,
		"cot":    `{"cot":"off","id":"x","lyrics":"[Verse]\nla la","seed":1101,"style":"pop, 100 BPM"}`,
		"cfg":    `{"cfg_scale":2,"cot":"full","id":"x","lyrics":"[Verse]\nla la","seed":1101,"style":"pop, 100 BPM"}`,
	} {
		c, r := Keys([]byte(req))
		if c == content || r == render {
			t.Errorf("%s change left a key unchanged", name)
		}
	}
}

// The keys are computed from M1's canonical bytes: a parsed request and its canonical form agree, including
// characters Go's default encoder would HTML-escape and an inline score.
func TestRecipeKeys_FromParsedCanonical(t *testing.T) {
	t.Parallel()
	p, err := yue2.ParseRequest([]byte(`{"id":"syn-b-s7","style":"jazz <&> 90 BPM","lyrics":"[Verse]\nok","cot":"melody","seed":7,"abc":"X:1\nK:C <&>\nCDEF|"}`), yue2.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c1, r1 := Keys(p.Canonical)
	if len(c1) != 64 || len(r1) != 64 {
		t.Fatalf("keys: %q %q", c1, r1)
	}
	// the same request with another id and key order canonicalises to the same keys
	q, err := yue2.ParseRequest([]byte(`{"seed":7,"cot":"melody","abc":"X:1\nK:C <&>\nCDEF|","lyrics":"[Verse]\nok","style":"jazz <&> 90 BPM","id":"syn-b-s8"}`), yue2.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c2, r2 := Keys(q.Canonical); c2 != c1 || r2 != r1 {
		t.Errorf("not canonical-stable: %s %s", c2, r2)
	}
}

func TestRecipeKeys_InvalidInput(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "null", "[]", "not json", `"str"`} {
		if c, r := Keys([]byte(in)); c != "" || r != "" {
			t.Errorf("Keys(%q) = %q %q, want empty", in, c, r)
		}
	}
}
