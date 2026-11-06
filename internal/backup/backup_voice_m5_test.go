package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestBackup_VoiceNotes (M5-V1): the frozen upload and its MP3 rendition are part of a snapshot (linked into the snapshot, hashed) and come
// back from a restore with mode 0444. The bytes are synthetic filler, not audio.
func TestBackup_VoiceNotes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	put := func(ext string, content string) (rel, sha string) {
		sum := sha256.Sum256([]byte(content))
		sha = hex.EncodeToString(sum[:])
		rel = "voice/" + sha[:2] + "/" + sha + "." + ext
		if err := os.MkdirAll(filepath.Dir(filepath.Join(e.data, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.data, rel), []byte(content), 0o444); err != nil {
			t.Fatal(err)
		}
		return rel, sha
	}
	orig, osha := put("webm", "synthetic upload bytes")
	mp3, msha := put("mp3", "synthetic rendition bytes")
	if _, err := e.st.W.Exec(`INSERT INTO voice_notes(id, track_id, sha256, bytes, container, codec, path, mp3_sha256, mp3_path, mp3_bytes, duration_s,
		principal_kind, principal_id, created_at) VALUES ('vn1', ?, ?, 22, 'webm', 'opus', ?, ?, ?, 25, 2.0, 'human', 'alice', '2026-01-01T00:00:00Z')`,
		lt.TrackShort, osha, orig, msha, mp3); err != nil {
		t.Fatal(err)
	}
	_, m, dir := e.run(t)
	have := map[string]ManifestFile{}
	for _, f := range m.Files {
		have[f.Path] = f
	}
	if f := have[orig]; f.Kind != "artifact" || f.SHA256 != osha || !f.Linked {
		t.Errorf("upload in manifest: %+v", f)
	}
	if f := have[mp3]; f.Kind != "rendition" || f.SHA256 != msha || !f.Linked {
		t.Errorf("rendition in manifest: %+v", f)
	}
	into := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(context.Background(), dir, into, restoreOpts(e)); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{orig, mp3} {
		if fi, err := os.Stat(filepath.Join(into, rel)); err != nil || fi.Mode().Perm() != 0o444 {
			t.Errorf("%s restored: %v %v", rel, fi, err)
		}
	}
}
