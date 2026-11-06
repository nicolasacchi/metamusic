package acceptance_test

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// voiceTone renders a synthetic tone (never a recording) as webm/opus with ffmpeg, base64 as the browser sends it.
func voiceTone(t *testing.T, seconds string) string {
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

// TestAcceptance_M5_VoiceNote: the walkie-talkie note is the operator's alone (roadmap M5, parity exception D-M5-23). The operator
// (and a paired phone) record a spoken fix at a second of a take; the agent reads it as an ordinary comment with a short-lived MP3 link
// and can write a Song PR against it; no agent can record one, over REST (403 HUMAN_ONLY) or MCP (the operation is not exposed: not a
// tool, not callable through call_operation); the stored original never leaves the server and the audit never holds audio.
func TestAcceptance_M5_VoiceNote(t *testing.T) {
	r := archiveRig(t, servertest.Options{M2b: servertest.M2bOptions{Media: true}})
	testtools.Require(t, "ffmpeg")
	h, phone := r.Human(t), r.Device(t)
	agentREST, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	agent := r.MCP(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	track := knownMother(t, r)
	path := "/api/tracks/" + track + "/voice-notes"
	audio := voiceTone(t, "2")

	// the agent cannot record: REST refuses, MCP has no such operation
	if resp := agentREST.Do(t, "POST", path, map[string]any{"audio_base64": audio}, "Idempotency-Key", "vn-agent"); resp.Status != 403 || obj(resp.JSON(t))["code"] != "HUMAN_ONLY" {
		t.Fatalf("agent over REST: %d %s", resp.Status, resp.Body)
	}
	if res := rawTool(t, agent, "call_operation", map[string]any{"operation_id": "create_voice_note", "arguments": map[string]any{"id": track, "audio_base64": audio}}, false); !res.IsError {
		t.Errorf("an agent recorded a voice note over MCP: %v", decodeStructured(t, res))
	}
	tools, err := agent.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if strings.Contains(tl.Name, "voice") {
			t.Errorf("tool %s exposes the voice note", tl.Name)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM voice_notes`); n != 0 {
		t.Fatalf("%d notes after the refusals", n)
	}

	// the operator records a spoken fix at 1.5 s; the phone records a note of its own
	resp := h.Do(t, "POST", path, map[string]any{"audio_base64": audio, "at_second": 1.5, "kind": "fix"}, "Idempotency-Key", "vn-1")
	if resp.Status != 201 {
		t.Fatalf("record: %d %s", resp.Status, resp.Body)
	}
	note := obj(resp.JSON(t))
	v := obj(note["voice"])
	if note["kind"] != "fix" || note["state"] != "open" || v["container"] != "webm" || v["codec"] != "opus" || v["duration_s"].(float64) < 1.8 {
		t.Fatalf("voice comment = %s", resp.Body)
	}
	if resp := phone.Do(t, "POST", path, map[string]any{"audio_base64": audio, "at_second": 0.5, "kind": "note"}, "Idempotency-Key", "vn-2"); resp.Status != 201 {
		t.Fatalf("phone: %d %s", resp.Status, resp.Body)
	}
	for _, leak := range []string{"voice/", r.DataDir, "sha256", ".webm"} {
		if strings.Contains(string(resp.Body), leak) {
			t.Errorf("response mentions %q", leak)
		}
	}

	// the agent sees both as comments with a link that plays as MP3, and the context carries the fix
	var fix map[string]any
	for _, c := range arr(obj(callTool(t, agent, "list_comments", map[string]any{"id": track}, true))["items"]) {
		if obj(c)["id"] == note["id"] {
			fix = obj(c)
		}
	}
	if fix == nil || obj(fix["voice"])["url"] == nil {
		t.Fatalf("the agent does not see the voice comment: %v", fix)
	}
	play := agentREST.Do(t, "GET", obj(fix["voice"])["url"].(string), nil)
	if play.Status != 200 || play.Header.Get("Content-Type") != "audio/mpeg" {
		t.Errorf("the agent's link: %d %s", play.Status, play.Header.Get("Content-Type"))
	}
	ctx := obj(obj(callTool(t, agent, "get_track_context", map[string]any{"id": track}, false))["judgments"])
	if ctx["open_fix"] != 1.0 {
		t.Errorf("context open_fix = %v, want the spoken fix", ctx["open_fix"])
	}

	// a spoken fix is addressable like a typed one: the agent writes a Song PR against it
	patch := obj(callTool(t, agent, "propose_patch", map[string]any{"base_track_id": track, "addresses_comment_ids": []string{note["id"].(string)}, "rationale": "the spoken fix",
		"extra_sections": []int{1}, "ops": []map[string]any{{"op": "replace_line", "section_idx": 1, "line_idx": 0, "text": "heard in the note"}}}, true))
	if a := arr(patch["addresses"]); len(a) != 1 || obj(a[0])["comment_id"] != note["id"] {
		t.Errorf("patch addresses = %v", patch["addresses"])
	}

	// the audit names the operation and holds no audio; the stored original is on disk and not served
	if n := count(t, r, `SELECT COUNT(*) FROM audit_log WHERE op = 'create_voice_note'`); n < 3 {
		t.Errorf("%d audit rows for create_voice_note, want the refusal and the two recordings", n)
	}
	text := dumpTables(t, r.Store.R, "audit_log")["audit_log"]
	if strings.Contains(text, audio[:64]) {
		t.Error("the audit log holds the audio")
	}
}
