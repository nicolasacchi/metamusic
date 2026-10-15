package subsonic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// archiveRig is a rig over a copy of the imported acceptance archive (7 lineages, 9 songs) with a read-write Subsonic
// client; it skips under -short and without the media tools like every acceptance test.
func archiveRig(t *testing.T) (*servertest.Rig, *subsonictest.Client, subsonictest.PlaceholderSet) {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	r := servertest.New(t, servertest.Options{Archive: &tpl})
	c := r.Subsonic(t, subsonictest.ClientOpts{Name: "Fixture Client", Username: "fixtureclient", Write: true})
	c.Clock = r.Clock
	ps := subsonictest.NewPlaceholders(t, r.Store)
	ps["user"] = c.Username
	ps["data_dir"] = r.DataDir
	return r, c, ps
}

// TestAcceptance_M4a_SubsonicClients replays the synthetic client files (what Amperfy, Symfonium and Tempus send,
// written from their documented behaviour) against a full server over the acceptance archive; each file is a subtest
// on its own rig and every step carries its own expectations.
func TestAcceptance_M4a_SubsonicClients(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("testdata", "clients", "*.jsonl"))
	if err != nil || len(files) < 3 {
		t.Fatalf("client fixtures: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		t.Run(filepath.Base(f[:len(f)-len(filepath.Ext(f))]), func(t *testing.T) {
			t.Parallel()
			_, c, ps := archiveRig(t)
			subsonictest.Replay(t, c, ps, f)
		})
	}
	if _, err := os.Stat(filepath.Join("testdata", "clients", "tempus.jsonl")); err != nil {
		t.Fatal(err)
	}
}

// song1 is the first visible song (C117.5 ordering) with its master bytes.
func song1(t *testing.T, ps subsonictest.PlaceholderSet) (id string, master []byte) {
	t.Helper()
	id = ps["song:1"]
	b, err := os.ReadFile(filepath.Join(ps["data_dir"], filepath.FromSlash(ps["master:"+id]))) //nolint:gosec // rig data directory
	if err != nil {
		t.Fatal(err)
	}
	return id, b
}

// TestAcceptance_M4a_SubsonicRawStreamSeek: a native client plays the raw 24-bit/48 kHz FLAC with seek: stream
// format=raw answers a 2-byte range with 206, the fLaC magic and a Content-Range over the master's size; a range from
// the middle equals the master's bytes; HEAD answers the headers without a body; getSong describes the file.
func TestAcceptance_M4a_SubsonicRawStreamSeek(t *testing.T) {
	t.Parallel()
	_, c, ps := archiveRig(t)
	id, master := song1(t, ps)
	size := int64(len(master))
	p := url.Values{"id": {id}, "format": {"raw"}, "maxBitRate": {"0"}}

	head := c.Call(t, "stream", p, subsonictest.CallOpts{Range: "bytes=0-1"})
	if head.HTTP != http.StatusPartialContent || !bytes.Equal(head.Body, []byte("fL")) ||
		head.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-1/%d", size) || head.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("bytes=0-1: %d %q %v", head.HTTP, head.Body, head.Header)
	}
	if !strings.HasPrefix(head.Header.Get("Content-Type"), "audio/") {
		t.Errorf("content type %q", head.Header.Get("Content-Type"))
	}
	mid := size / 2
	seek := c.Call(t, "stream", p, subsonictest.CallOpts{Range: fmt.Sprintf("bytes=%d-", mid)})
	if seek.HTTP != http.StatusPartialContent || !bytes.Equal(seek.Body, master[mid:]) ||
		seek.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", mid, size-1, size) {
		t.Fatalf("bytes=%d-: %d, %d bytes, %q", mid, seek.HTTP, len(seek.Body), seek.Header.Get("Content-Range"))
	}
	whole := c.Call(t, "stream", p, subsonictest.CallOpts{})
	if whole.HTTP != http.StatusOK || !bytes.Equal(whole.Body, master) {
		t.Fatalf("whole stream: %d, %d bytes (want %d)", whole.HTTP, len(whole.Body), size)
	}

	q := url.Values(p)
	q.Set("apiKey", c.APIKey)
	q.Set("c", "subsonictest")
	q.Set("v", "1.16.1")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, c.BaseURL+"/rest/stream?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || n != 0 || resp.Header.Get("Content-Length") != fmt.Sprint(size) {
		t.Errorf("HEAD: %d, %d body bytes, Content-Length %q", resp.StatusCode, n, resp.Header.Get("Content-Length"))
	}

	song := c.Call(t, "getSong", url.Values{"id": {id}}, subsonictest.CallOpts{JSON: true})
	doc, _ := song.Doc["song"].(map[string]any)
	if song.Status != "ok" || doc["bitDepth"] != float64(24) || doc["samplingRate"] != float64(48000) || doc["suffix"] != "flac" {
		t.Errorf("getSong: %v", song.Doc)
	}
}

// TestAcceptance_M4a_SubsonicRatingSync: setRating writes the operator's rating as a device, 0 clears it, the critic's
// row is never touched; star makes the track final and getStarred2 lists it; unstar sends it back to to_relisten.
func TestAcceptance_M4a_SubsonicRatingSync(t *testing.T) {
	t.Parallel()
	r, c, ps := archiveRig(t)
	id := ps["song:1"]
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	if resp := agent.Do(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 2}, "Idempotency-Key", "critic-1"); resp.Status != 200 {
		t.Fatalf("critic rating: %d %s", resp.Status, resp.Body)
	}
	rating := func() (human, critic map[string]any) {
		t.Helper()
		resp := r.Human(t).Do(t, "GET", "/api/tracks/"+id, nil)
		var tr struct {
			Status string `json:"status"`
			Rating struct {
				Human  map[string]any `json:"human"`
				Critic map[string]any `json:"critic"`
			} `json:"rating"`
		}
		if resp.Status != 200 || json.Unmarshal(resp.Body, &tr) != nil {
			t.Fatalf("get_track: %d %s", resp.Status, resp.Body)
		}
		return tr.Rating.Human, tr.Rating.Critic
	}
	status := func() string {
		t.Helper()
		var s string
		if err := r.Store.R.QueryRow(`SELECT status FROM tracks WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	_, critic := rating()
	if critic == nil || critic["stars"] != float64(2) {
		t.Fatalf("critic row before: %v", critic)
	}

	ok := func(name string, p url.Values) {
		t.Helper()
		if resp := c.Call(t, name, p, subsonictest.CallOpts{JSON: true}); resp.Status != "ok" {
			t.Fatalf("%s: %d %q %.200s", name, resp.HTTP, resp.Status, resp.Body)
		}
	}
	ok("setRating", url.Values{"id": {id}, "rating": {"4"}})
	h, critic := rating()
	by, _ := h["by"].(map[string]any)
	if h == nil || h["stars"] != float64(4) || by["kind"] != "device" {
		t.Fatalf("human row after setRating 4: %v", h)
	}
	if critic["stars"] != float64(2) {
		t.Errorf("critic row changed: %v", critic)
	}
	ok("setRating", url.Values{"id": {id}, "rating": {"0"}})
	if h, critic = rating(); h != nil && h["stars"] != nil {
		t.Errorf("setRating 0 left the rating: %v", h)
	}
	if critic["stars"] != float64(2) {
		t.Errorf("critic row changed by the clear: %v", critic)
	}

	ok("star", url.Values{"id": {id}})
	if status() != "final" {
		t.Errorf("status after star = %q, want final", status())
	}
	starred := c.Call(t, "getStarred2", nil, subsonictest.CallOpts{JSON: true})
	if got := subsonictest.CountAt(starred.Doc, "starred2.song"); got != 1 {
		t.Errorf("getStarred2 lists %d songs, want 1: %.300s", got, starred.Body)
	}
	if v, _ := subsonictest.ScalarAt(starred.Doc, "starred2.song.0.id"); v != id {
		t.Errorf("getStarred2 lists %q, want %q", v, id)
	}
	ok("unstar", url.Values{"id": {id}})
	if status() != "to_relisten" {
		t.Errorf("status after unstar = %q, want to_relisten", status())
	}
}
