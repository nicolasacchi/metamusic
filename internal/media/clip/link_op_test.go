package clip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func jsonObj(t testing.TB, r *servertest.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("body %q: %v", r.Body, err)
	}
	return m
}

func clipURL(id string, q string) string {
	p := "/api/tracks/" + id + "/clip"
	if q != "" {
		p += "?" + q
	}
	return p
}

type fetched struct {
	StatusCode int
	Header     http.Header
}

func get(t testing.TB, base, link string, hdr ...string) (fetched, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", base+link, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return fetched{resp.StatusCode, resp.Header}, b
}

func verifyLink(t testing.TB, r *servertest.Rig, link, key string) {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/media/"+key+".flac" {
		t.Fatalf("link path %q, want /media/%s.flac", u.Path, key)
	}
	if err := r.Services.Server.Signer.Verify(key, "flac", u.Query().Get("exp"), u.Query().Get("sig")); err != nil {
		t.Errorf("signature: %v", err)
	}
}

func TestClipLink_Op(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2b: servertest.M2bOptions{Fixture: true}})
	agent, _ := r.Agent(t, auth.ScopeRead)

	resp := agent.Do(t, "GET", clipURL(lt.TrackLuna, ""), nil)
	if resp.Status != 200 {
		t.Fatalf("get_clip: %d %s", resp.Status, resp.Body)
	}
	l := jsonObj(t, resp)
	key := l["key"].(string)
	verifyLink(t, r, l["url"].(string), key)
	if l["content_type"] != "audio/flac" || l["sample_rate"] != 48000.0 || l["bits_per_sample"] != 24.0 || l["channels"] != 2.0 ||
		l["from_s"] != 0.0 || l["to_s"] != 12.0 || l["duration_s"] != 12.0 || l["match"] != "off" || l["gain_db"] != 0.0 ||
		l["ttl_s"] != 900.0 || l["window_lufs"] != nil || l["target_lufs"] != nil {
		t.Errorf("agent link = %v", l)
	}
	// the link works and serves the rendered file
	if hr, body := get(t, r.URL, l["url"].(string)); hr.StatusCode != 200 || hr.Header.Get("Content-Type") != "audio/flac" || int64(len(body)) != int64(l["bytes"].(float64)) {
		t.Errorf("download: %d %v (%d bytes)", hr.StatusCode, hr.Header, len(body))
	}

	// humans and paired devices get the long TTL
	for name, c := range map[string]*servertest.Client{"human": r.Human(t), "device": r.Device(t)} {
		resp := c.Do(t, "GET", clipURL(lt.TrackLuna, ""), nil)
		if resp.Status != 200 || jsonObj(t, resp)["ttl_s"] != 43200.0 {
			t.Errorf("%s: %d %s, want ttl_s 43200", name, resp.Status, resp.Body)
		}
	}

	// match_lufs attenuates only: the fake analyzer measures -20 LUFS
	m := jsonObj(t, agent.Do(t, "GET", clipURL(lt.TrackLuna, "match_lufs=-30"), nil))
	if m["match"] != "matched" || m["gain_db"] != -10.0 || m["window_lufs"] != -20.0 || m["target_lufs"] != -30.0 || m["key"] == key {
		t.Errorf("match -30 = %v", m)
	}
	m = jsonObj(t, agent.Do(t, "GET", clipURL(lt.TrackLuna, "match_lufs=-10"), nil))
	if m["match"] != "limited" || m["gain_db"] != 0.0 || m["target_lufs"] != -10.0 || m["key"] != key {
		t.Errorf("match -10 = %v", m)
	}

	for name, c := range map[string]struct {
		path, code string
		status     int
	}{
		"unknown track":    {clipURL("01900000-0000-7000-8000-00000000ffff", ""), "NOT_FOUND", 404},
		"window too short": {clipURL(lt.TrackLuna, "from_s=0&to_s=5"), "CLIP_WINDOW_INVALID", 422},
		"match too low":    {clipURL(lt.TrackLuna, "match_lufs=-50"), "", 422},
		"match too high":   {clipURL(lt.TrackLuna, "match_lufs=-5"), "", 422},
	} {
		resp := agent.Do(t, "GET", c.path, nil)
		if resp.Status != c.status || (c.code != "" && jsonObj(t, resp)["code"] != c.code) {
			t.Errorf("%s: %d %s, want %d %s", name, resp.Status, resp.Body, c.status, c.code)
		}
	}
	if w := jsonObj(t, agent.Do(t, "GET", clipURL(lt.TrackLuna, "from_s=0&to_s=5"), nil))["details"].(map[string]any); w["duration_s"] != 12.0 || w["min_s"] != 12.0 || w["max_s"] != 12.0 {
		t.Errorf("window details = %v", w)
	}
}

func TestClipLink_HeavyBucket(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, Limits: ratelimit.Default})
	agent, _ := r.Agent(t, auth.ScopeRead)
	for i := 1; i <= 6; i++ {
		if resp := agent.Do(t, "GET", clipURL(lt.TrackLuna, ""), nil); resp.Status != 200 {
			t.Fatalf("call %d: %d %s", i, resp.Status, resp.Body)
		}
	}
	resp := agent.Do(t, "GET", clipURL(lt.TrackLuna, ""), nil)
	if resp.Status != 429 || jsonObj(t, resp)["details"].(map[string]any)["bucket"] != "agent_heavy" {
		t.Errorf("7th call: %d %s, want 429 agent_heavy", resp.Status, resp.Body)
	}
}

func TestClipLink_Unavailable(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2b: servertest.M2bOptions{Override: func(s *app.Services) { s.Clips = nil }}})
	agent, _ := r.Agent(t, auth.ScopeRead)
	resp := agent.Do(t, "GET", clipURL(lt.TrackLuna, ""), nil)
	if d, _ := jsonObj(t, resp)["details"].(map[string]any); resp.Status != 503 || d["component"] != "clips" {
		t.Errorf("no clips: %d %s, want 503 {component: clips}", resp.Status, resp.Body)
	}
}

func TestClipLink_BareService(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := clip.New(e.root, e.dir, mediatool.Tools{}, failingAnalyzer{}, nil, nil, clip.Options{})
	if _, err := s.Link(context.Background(), auth.Principal{}, "x", clip.Query{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("a service without library and signer: %v, want ErrUnsupported", err)
	}
}

// TestMediaHandler_ServesClip drives the full resolver chain (library, exports,
// clips) through the media handler.
func TestMediaHandler_ServesClip(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2b: servertest.M2bOptions{Fixture: true}})
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	l := jsonObj(t, agent.Do(t, "GET", clipURL(lt.TrackLuna, ""), nil))
	bytesN := int(l["bytes"].(float64))

	hr, body := get(t, r.URL, l["url"].(string))
	if hr.StatusCode != 200 || hr.Header.Get("Content-Type") != "audio/flac" || len(body) != bytesN || !bytes.HasPrefix(body, []byte("fLaC")) {
		t.Fatalf("clip: %d %v len %d", hr.StatusCode, hr.Header, len(body))
	}
	hr, part := get(t, r.URL, l["url"].(string), "Range", "bytes=0-9")
	if hr.StatusCode != 206 || len(part) != 10 {
		t.Errorf("range: %d, %d bytes", hr.StatusCode, len(part))
	}
	// a master sha still resolves through the library first
	var sha string
	if err := r.Store.R.QueryRow(`SELECT master_sha256 FROM tracks WHERE id = ?`, lt.TrackLuna).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	mu := r.Services.Server.Signer.Sign(sha, "flac", 3600e9, 60e9).URL
	if hr, _ := get(t, r.URL, mu); hr.StatusCode != 200 {
		t.Errorf("master: %d", hr.StatusCode)
	}
	// an export .jsonl resolves through exports
	x := jsonObj(t, agent.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "source", "source_path": "music/songs.jsonl"}, "Idempotency-Key", "e1"))
	if hr, _ := get(t, r.URL, x["download"].(map[string]any)["url"].(string)); hr.StatusCode != 200 {
		t.Errorf("export: %d", hr.StatusCode)
	}
	// an unsigned or unknown clip address is refused
	if hr, _ := get(t, r.URL, "/media/"+l["key"].(string)+".flac"); hr.StatusCode != 403 {
		t.Errorf("unsigned: %d", hr.StatusCode)
	}
	unknown := r.Services.Server.Signer.Sign(strings.Repeat("cd", 32), "flac", 3600e9, 60e9).URL
	if hr, _ := get(t, r.URL, unknown); hr.StatusCode != 404 {
		t.Errorf("unknown clip: %d", hr.StatusCode)
	}
}

// TestAcceptance_M2b_ClipContract runs the C46 contract over REST against a
// real 240 s master with real ffmpeg.
func TestAcceptance_M2b_ClipContract(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg")
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2b: servertest.M2bOptions{Media: true}})
	tools := testtools.Tools()

	// swap the fixture's Luna master for a real 240 s tone; the test DB's frozen-column
	// trigger is dropped so the row can carry the real duration (throwaway database).
	var rel string
	if err := r.Store.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, lt.TrackLuna).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	master := filepath.Join(r.DataDir, filepath.FromSlash(rel))
	if err := os.Remove(master); err != nil {
		t.Fatal(err)
	}
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-f", "lavfi", "-i", "sine=f=440:d=240:r=48000,aformat=channel_layouts=stereo",
		"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", "-f", "flac", "file:" + master}
	if _, err := mediatool.Run(context.Background(), tools.FFmpeg, args, mediatool.RunOpts{Timeout: 120e9, MaxStdout: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TRIGGER tracks_frozen`, `UPDATE tracks SET duration_s = 240, total_samples = 11520000, sample_rate = 48000, channels = 2 WHERE id = '` + lt.TrackLuna + `'`} {
		if _, err := r.Store.W.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	agent, _ := r.Agent(t, auth.ScopeRead)
	resp := agent.Do(t, "GET", clipURL(lt.TrackLuna, ""), nil)
	if resp.Status != 200 {
		t.Fatalf("default: %d %s", resp.Status, resp.Body)
	}
	l := jsonObj(t, resp)
	if l["from_s"] != 72.0 || l["to_s"] != 117.0 || l["duration_s"] != 45.0 || l["bits_per_sample"] != 24.0 || l["sample_rate"] != 48000.0 {
		t.Errorf("default window = %v", l)
	}
	key := l["key"].(string)
	verifyLink(t, r, l["url"].(string), key)
	_, data := get(t, r.URL, l["url"].(string))
	info, err := flacmeta.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if si := info.StreamInfo; si.BitsPerSample != 24 || si.SampleRate != 48000 || si.TotalSamples != 45*48000 {
		t.Errorf("streaminfo = %+v", si)
	}
	cached := filepath.Join(r.DataDir, "derived", "clips", key[:2], key+".flac")
	st1, err := os.Stat(cached)
	if err != nil {
		t.Fatal(err)
	}
	l2 := jsonObj(t, agent.Do(t, "GET", clipURL(lt.TrackLuna, ""), nil))
	u1, _ := url.Parse(l["url"].(string))
	u2, _ := url.Parse(l2["url"].(string))
	st2, _ := os.Stat(cached)
	if u1.Path != u2.Path || !os.SameFile(st1, st2) || !st1.ModTime().Equal(st2.ModTime()) {
		t.Errorf("second call: path %q vs %q, same file %v", u1.Path, u2.Path, os.SameFile(st1, st2))
	}

	if l := jsonObj(t, agent.Do(t, "GET", clipURL(lt.TrackLuna, "from_s=0&to_s=30"), nil)); l["duration_s"] != 30.0 {
		t.Errorf("[0, 30] = %v", l)
	}
	for _, q := range []string{"from_s=0&to_s=29.9", "from_s=0&to_s=45.1"} {
		if resp := agent.Do(t, "GET", clipURL(lt.TrackLuna, q), nil); resp.Status != 422 || jsonObj(t, resp)["code"] != "CLIP_WINDOW_INVALID" {
			t.Errorf("%s: %d %s, want 422 CLIP_WINDOW_INVALID", q, resp.Status, resp.Body)
		}
	}

	ml := jsonObj(t, agent.Do(t, "GET", clipURL(lt.TrackLuna, "match_lufs=-30"), nil))
	_, mdata := get(t, r.URL, ml["url"].(string))
	out := filepath.Join(t.TempDir(), "matched.flac")
	if err := os.WriteFile(out, mdata, 0o600); err != nil {
		t.Fatal(err)
	}
	got := measureClip(t, tools, out, 45*48000)
	if math.Abs(got.IntegratedLUFS-(-30)) > 0.2 {
		t.Errorf("match_lufs=-30 measures %.2f LUFS", got.IntegratedLUFS)
	}
}
