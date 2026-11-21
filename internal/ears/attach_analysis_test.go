package ears_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// received puts a document where the upload service would have left it (under the data root) and returns its path, digest and size.
func (r *rig) received(name string, doc []byte) (path, sha string, size int64) {
	r.t.Helper()
	if err := r.root.MkdirAll("uploads", 0o750); err != nil {
		r.t.Fatal(err)
	}
	path = "uploads/" + name
	if err := r.root.WriteFile(path, doc, 0o600); err != nil {
		r.t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	return path, hex.EncodeToString(sum[:]), int64(len(doc))
}

// attach runs AttachAnalysis in a transaction that commits (or rolls back when commit is false).
func (r *rig) attach(track, path, sha string, size int64, commit bool) (ears.AttachedAnalysis, error) {
	r.t.Helper()
	tx, err := r.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	a, err := r.svc.AttachAnalysis(context.Background(), tx, track, path, sha, size)
	if err != nil || !commit {
		_ = tx.Rollback()
		return a, err
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
	return a, nil
}

func (r *rig) artifactRows(track string) []string {
	r.t.Helper()
	rows, err := r.st.R.Query(`SELECT name FROM artifacts WHERE track_id = ? AND name LIKE 'analysis%.json' ORDER BY name`, track)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// TestAttachAnalysis_StoresFrozenDocument: the document lands at takes/<id>/analysis.json (0444, directory created for an import that
// had none) with an artifacts row of its digest, and nothing else of the take changes (masters stay frozen, no recipe is touched).
func TestAttachAnalysis_StoresFrozenDocument(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	const track = lt.TrackCitta // an import has no takes/<id> directory yet
	if err := os.RemoveAll(filepath.Join(r.dir, "takes", track)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "takes", track)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the reference already has a directory: %v", err)
	}
	doc := tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{})
	path, sha, size := r.received("a.json", doc)
	master := r.str(`SELECT master_sha256 FROM tracks WHERE id = ?`, track)
	recipes, tracks := r.count(`SELECT COUNT(*) FROM recipes`), r.count(`SELECT COUNT(*) FROM tracks`)

	a, err := r.attach(track, path, sha, size, true)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "analysis.json" || a.Path != "takes/"+track+"/analysis.json" || a.Duplicate {
		t.Fatalf("attached = %+v", a)
	}
	st, err := os.Stat(filepath.Join(r.dir, a.Path))
	if err != nil || st.Mode().Perm() != 0o444 || st.Size() != size {
		t.Errorf("stored file: %v %v", st, err)
	}
	if got := r.str(`SELECT sha256 FROM artifacts WHERE track_id = ? AND name = 'analysis.json'`, track); got != sha {
		t.Errorf("artifact sha = %s, want %s", got, sha)
	}
	if r.str(`SELECT master_sha256 FROM tracks WHERE id = ?`, track) != master || r.count(`SELECT COUNT(*) FROM recipes`) != recipes || r.count(`SELECT COUNT(*) FROM tracks`) != tracks {
		t.Error("attaching a document changed a master, a recipe or the track count")
	}
	// the stored document reads back for the analysis
	if _, err := r.svc.AnalyzeTierB(context.Background(), track); err != nil {
		t.Errorf("AnalyzeTierB after attach: %v", err)
	}
}

// TestAttachAnalysis_DuplicateRace: a second document for a take that already has one is stored as analysis-2.json and reported as a
// duplicate; the first is never overwritten.
func TestAttachAnalysis_DuplicateRace(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	first := tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{})
	second := tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{StartS: 2})
	p1, s1, n1 := r.received("1.json", first)
	p2, s2, n2 := r.received("2.json", second)
	if _, err := r.attach(lt.TrackLuna, p1, s1, n1, true); err != nil {
		t.Fatal(err)
	}
	a, err := r.attach(lt.TrackLuna, p2, s2, n2, true)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "analysis-2.json" || !a.Duplicate {
		t.Fatalf("second = %+v", a)
	}
	if got := r.artifactRows(lt.TrackLuna); len(got) != 2 {
		t.Errorf("artifact rows = %v", got)
	}
	b, _ := os.ReadFile(filepath.Join(r.dir, "takes", lt.TrackLuna, "analysis.json"))
	if string(b) != string(first) {
		t.Error("the first document was overwritten")
	}
}

// TestAttachAnalysis_Refusals: a document that does not match its digest or size, or is not an mm.ears-b/1 document, a missing file and
// an unknown track are refused and leave no file and no row.
func TestAttachAnalysis_Refusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	good := tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{})
	gp, gs, gn := r.received("good.json", good)
	bp, bs, bn := r.received("bad.json", []byte(`{"contract":"mm.ears-b/0","garbage":true}`))
	for name, c := range map[string]struct {
		track, path, sha string
		size             int64
		want             error
	}{
		"wrong digest": {lt.TrackLuna, gp, gs[:63] + "0", gn, ears.ErrAttachMismatch},
		"wrong size":   {lt.TrackLuna, gp, gs, gn + 1, ears.ErrAttachMismatch},
		"garbage":      {lt.TrackLuna, bp, bs, bn, tierb.ErrContract},
		"missing file": {lt.TrackLuna, "uploads/none.json", gs, gn, fs.ErrNotExist},
		"unknown take": {"01900000-0000-7000-8000-00000000ffff", gp, gs, gn, library.ErrNoSuchTrack},
	} {
		_, err := r.attach(c.track, c.path, c.sha, c.size, true)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", name, err, c.want)
		}
	}
	if rows := r.artifactRows(lt.TrackLuna); len(rows) != 0 {
		t.Errorf("a refused document left rows %v", rows)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "takes", lt.TrackLuna, "analysis.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused document left a file: %v", err)
	}
}

// TestAttachAnalysis_RollbackAndOrphan: a rolled-back transaction leaves an orphan file that no row names; the next attach takes
// the name over (no row, nothing to lose) instead of failing.
func TestAttachAnalysis_RollbackAndOrphan(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	doc := tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{})
	p, s, n := r.received("a.json", doc)
	if _, err := r.attach(lt.TrackLuna, p, s, n, false); err != nil {
		t.Fatal(err)
	}
	if rows := r.artifactRows(lt.TrackLuna); len(rows) != 0 {
		t.Fatalf("rows after rollback: %v", rows)
	}
	a, err := r.attach(lt.TrackLuna, p, s, n, true)
	if err != nil || a.Name != "analysis.json" || a.Duplicate {
		t.Fatalf("retry = %+v %v", a, err)
	}
	var one int
	if err := r.st.R.QueryRow(`SELECT 1 FROM artifacts WHERE track_id = ? AND name = 'analysis.json'`, lt.TrackLuna).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		t.Error("no row after the retry")
	}
}
