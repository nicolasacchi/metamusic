package subsonictest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCountAtAndScalarAt(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"a": map[string]any{"list": []any{map[string]any{"id": "x"}, map[string]any{"id": "y"}}, "one": map[string]any{"id": 7.0}}}
	for path, want := range map[string]int{"a.list": 2, "a.one": 1, "a.none": 0, "a.list.1": 1, "a.list.9": 0, "b": 0, "a.list.1.id.deeper": 0} {
		if got := CountAt(doc, path); got != want {
			t.Errorf("CountAt(%q) = %d, want %d", path, got, want)
		}
	}
	if v, ok := ScalarAt(doc, "a.list.1.id"); !ok || v != "y" {
		t.Errorf("ScalarAt list = %q, %v", v, ok)
	}
	if v, ok := ScalarAt(doc, "a.one.id"); !ok || v != "7" {
		t.Errorf("ScalarAt number = %q, %v", v, ok)
	}
	if _, ok := ScalarAt(doc, "a.list"); ok {
		t.Error("ScalarAt returned a list")
	}
}

func TestPlaceholderExpand(t *testing.T) {
	t.Parallel()
	ps := PlaceholderSet{"song:1": "s1", "mid": "40", "master:s1": "x.flac"}
	if got, err := ps.expand("bytes={{mid}}-/{{song:1}}"); err != nil || got != "bytes=40-/s1" {
		t.Errorf("expand = %q, %v", got, err)
	}
	for _, bad := range []string{"{{nope}}", "{{mid", "{{master:s1}}"} {
		if _, err := ps.expand(bad); err == nil {
			t.Errorf("expand(%q) succeeded", bad)
		}
	}
}

type fakeClock struct{ total time.Duration }

func (f *fakeClock) Advance(d time.Duration) { f.total += d }

// TestReplay_AgainstStub drives Replay over a stub server: salts are reused by label, the clock advances, values are
// saved for later steps and a mismatch fails the test (checked through a recording TB).
func TestReplay_AgainstStub(t *testing.T) {
	t.Parallel()
	var salts []string
	var lastPlaylist string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		salts = append(salts, r.URL.Query().Get("s"))
		lastPlaylist = r.URL.Query().Get("id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok","playlist":{"id":"pl1","entry":[{"id":"a"},{"id":"b"}]}}}`))
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "x.jsonl")
	lines := `{"step":"one","path":"/rest/createPlaylist","auth":"token","params":{"f":["json"]},"expect":{"http":200,"status":"ok","counts":{"playlist.entry":2}},"save":{"pl":"playlist.id"}}
{"step":"two","path":"/rest/getPlaylist.view","auth":"token","salt":"one","advance":"11m","params":{"id":["{{pl}}"],"f":["json"]},"expect":{"status":"ok"}}
{"step":"three","path":"/rest/ping","auth":"token","skip":"later","expect":{"http":418}}
`
	if err := os.WriteFile(file, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{}
	c := &Client{BaseURL: srv.URL, Username: "u1", Password: "pw", Clock: clk}
	Replay(t, c, PlaceholderSet{}, file)
	if len(salts) != 2 || salts[0] == "" || salts[0] != salts[1] || clk.total != 11*time.Minute || lastPlaylist != "pl1" {
		t.Errorf("salts %v, advanced %v, id %q", salts, clk.total, lastPlaylist)
	}
	if saltFor("one") == saltFor("two") {
		t.Error("distinct labels share a salt")
	}
}
