package acestep

import (
	"encoding/json"
	"flag"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
)

var update = flag.Bool("update", false, "rewrite testdata golden files")

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fullCaps are the caps of an engine whose canary proved everything and whose base models are delivered.
func fullCaps() engine.Caps {
	f := EmbeddedFeatures()
	f.Tasks = []string{"text2music", "repaint", "extend", "complete", "extract"}
	f.CanaryCommit = "c0ffee"
	f.Reference, f.LRC, f.SeedReproducible = true, true, true
	return Engine{Verified: f}.Caps()
}

type obj map[string]any

// req builds a request JSON from a valid text2music base with the overrides applied (nil deletes a key).
func req(over obj) json.RawMessage {
	m := obj{"id": "syn-a", "task": "text2music", "caption": "warm pop, female vocal", "lyrics": "[Verse]\nla la la",
		"instrumental": false, "vocal_language": "en", "duration_ds": 1800, "seed": 7, "thinking": true}
	for k, v := range over {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func src(ds int) obj {
	return obj{"track_id": "trk-1", "master_sha256": shaA, "duration_ds": ds}
}

func repaintOver(from, to int) obj {
	return obj{"task": "repaint", "duration_ds": nil, "thinking": false, "src": src(1000), "repaint_from_ds": from, "repaint_to_ds": to}
}

func extendOver(ext int) obj {
	return obj{"task": "extend", "duration_ds": nil, "thinking": false, "src": src(2431), "extend_ds": ext}
}

func completeOver(tracks ...string) obj {
	return obj{"task": "complete", "duration_ds": nil, "thinking": false, "lyrics": "", "instrumental": true, "src": src(1000), "tracks": tracks}
}

func codes(is []engine.Issue) string {
	var out []string
	for _, i := range is {
		if i.Severity == "error" {
			out = append(out, i.Code+"@"+i.Field)
		}
	}
	return strings.Join(out, ",")
}

func mustParse(t *testing.T, raw json.RawMessage) (Request, []byte) {
	t.Helper()
	r, c, is := Parse(raw)
	if HasErrors(is) {
		t.Fatalf("Parse: %v", is)
	}
	return r, c
}
