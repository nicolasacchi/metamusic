package acceptance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// seedTierBDocument gives a take a stored analysis.json, as the ingest bridge stores a render's, and analyses it (the way the take
// hook does after the commit), so the scripted agent call of get_track_tier_b has a listening check to read. The recognised words are the
// take's own lyrics, faithfully.
func seedTierBDocument(t *testing.T, r *servertest.Rig, trackID string) {
	t.Helper()
	var lyrics string
	if err := r.Store.R.QueryRow(`SELECT rc.lyrics FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE t.id = ?`, trackID).Scan(&lyrics); err != nil {
		t.Fatal(err)
	}
	doc := tierbtest.FromLyrics(lyrics, tierbtest.Opts{})
	dir := filepath.Join(r.DataDir, "takes", trackID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "analysis.json"), doc, 0o444); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	if _, err := r.Store.W.Exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?, 'analysis.json', ?, ?, ?, ?)`,
		trackID, hex.EncodeToString(sum[:]), len(doc), "takes/"+trackID+"/analysis.json", store.FormatTime(r.Clock.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Services.Ears.AnalyzeTierB(t.Context(), trackID); err != nil {
		t.Fatal(err)
	}
}
