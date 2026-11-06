package api_test

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// implementedVoiceM5 lists the M5 operations whose 501 stub lane M5-V1 has replaced (ids from m5Ops in ops_m5_test.go). The lane
// adds its ids here, drops the matching 501 parity cases and covers the behaviour in its own tests; it owns this file alone.
var implementedVoiceM5 = map[string]bool{"create_voice_note": true}

// voiceFixture renders a synthetic tone (never a recording) as webm/opus with the pinned ffmpeg.
func voiceFixture(t *testing.T, seconds string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "n.webm")
	args := []string{"-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=330:duration=" + seconds, "-ac", "1", "-c:a", "libopus", "-b:a", "16k", "-f", "webm", out}
	if _, err := mediatool.Run(context.Background(), testtools.Tools().FFmpeg, args, mediatool.RunOpts{Timeout: 60 * time.Second, MaxStdout: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func voiceRig(t *testing.T) *servertest.Rig {
	t.Helper()
	testtools.Require(t, "ffmpeg")
	return servertest.New(t, servertest.Options{Fixture: true, Files: true, M2b: servertest.M2bOptions{Media: true}})
}

// TestVoice_REST: the operator records a note on a track; the comment reads back with a signed MP3 link that plays, an agent is
// refused with 403 HUMAN_ONLY, replays are idempotent and nothing in the answer reveals a path.
func TestVoice_REST(t *testing.T) {
	r := voiceRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, rw...)
	path := "/api/tracks/" + lt.TrackShort + "/voice-notes"
	body := map[string]any{"audio_base64": voiceFixture(t, "2"), "at_second": 1.0, "kind": "keep"}

	if resp := agent.Do(t, "POST", path, body, "Idempotency-Key", "v-agent"); resp.Status != 403 || obj(t, resp.JSON(t))["code"] != "HUMAN_ONLY" {
		t.Fatalf("agent: %d %s", resp.Status, resp.Body)
	}
	resp := human.Do(t, "POST", path, body, "Idempotency-Key", "v-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	c := obj(t, resp.JSON(t))
	v := obj(t, c["voice"])
	if c["kind"] != "keep" || c["at_second"] != 1.0 || c["state"] != "open" || c["source"] != "api" && c["source"] != "web" ||
		v["container"] != "webm" || v["codec"] != "opus" || v["duration_s"].(float64) < 1.8 {
		t.Fatalf("comment %s", resp.Body)
	}
	url, _ := v["url"].(string)
	for _, leak := range []string{"voice/", "incoming", r.DataDir, "sha256", "path"} {
		if strings.Contains(string(resp.Body), leak) {
			t.Errorf("response mentions %q: %s", leak, resp.Body)
		}
	}
	if play := human.Do(t, "GET", url, nil); play.Status != 200 || play.Header.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("play: %d %s", play.Status, play.Header.Get("Content-Type"))
	}
	// idempotent replay: same answer, no second note
	if again := human.Do(t, "POST", path, body, "Idempotency-Key", "v-1"); again.Status != 201 || obj(t, obj(t, again.JSON(t))["voice"])["id"] != v["id"] {
		t.Fatalf("replay: %d %s", again.Status, again.Body)
	}
	var n int
	_ = r.Store.R.QueryRow(`SELECT COUNT(*) FROM voice_notes`).Scan(&n)
	if n != 1 {
		t.Errorf("%d notes after a replay", n)
	}
	// list_comments carries the voice object, to agents too, and an agent's link is short-lived but plays
	list := agent.Do(t, "GET", "/api/tracks/"+lt.TrackShort+"/comments", nil)
	if list.Status != 200 || !strings.Contains(string(list.Body), `"voice"`) {
		t.Fatalf("list: %d %s", list.Status, list.Body)
	}
	for _, it := range obj(t, list.JSON(t))["items"].([]any) {
		if vv, ok := obj(t, it)["voice"].(map[string]any); ok {
			if p := agent.Do(t, "GET", vv["url"].(string), nil); p.Status != 200 {
				t.Errorf("agent play: %d", p.Status)
			}
		}
	}
	// GET never mutates
	var after int
	_ = r.Store.R.QueryRow(`SELECT COUNT(*) FROM voice_notes`).Scan(&after)
	if after != 1 {
		t.Errorf("reads changed the notes: %d", after)
	}
}

func TestVoice_REST_Errors(t *testing.T) {
	r := voiceRig(t)
	human := r.Human(t)
	path := "/api/tracks/" + lt.TrackShort + "/voice-notes"
	for name, tc := range map[string]struct {
		path   string
		body   map[string]any
		status int
		code   string
		reason string
	}{
		"not base64": {path, map[string]any{"audio_base64": "%%%"}, 422, "VOICE_INVALID", "encoding"},
		"not audio":  {path, map[string]any{"audio_base64": base64.StdEncoding.EncodeToString([]byte("plain text, not audio"))}, 422, "VOICE_INVALID", "container"},
		"no track":   {"/api/tracks/01900000-0000-7000-8000-00000000ffff/voice-notes", map[string]any{"audio_base64": voiceFixture(t, "1")}, 404, "", ""},
		"past end":   {path, map[string]any{"audio_base64": voiceFixture(t, "1"), "at_second": 9999.0}, 422, "AT_SECOND_OUT_OF_RANGE", ""},
		"bad kind":   {path, map[string]any{"audio_base64": voiceFixture(t, "1"), "kind": "marker"}, 422, "", ""},
	} {
		resp := human.Do(t, "POST", tc.path, tc.body, "Idempotency-Key", "e-"+strings.ReplaceAll(name, " ", "-"))
		m := obj(t, resp.JSON(t))
		if resp.Status != tc.status || (tc.code != "" && m["code"] != tc.code) {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
		if tc.reason != "" && obj(t, m["details"])["reason"] != tc.reason {
			t.Errorf("%s: reason in %s", name, resp.Body)
		}
	}
}
