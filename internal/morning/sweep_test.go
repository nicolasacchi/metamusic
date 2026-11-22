package morning

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// side records a ready crate (created age ago) with one side file under derived/morning, shared by sha.
func (r *rig) side(crate, sha string, age time.Duration) string {
	r.t.Helper()
	rel := "derived/morning/" + sha[:2] + "/" + sha + ".flac"
	if err := r.data.MkdirAll("derived/morning/"+sha[:2], 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := r.data.WriteFile(rel, []byte("side"), 0o444); err != nil {
		r.t.Fatal(err)
	}
	at := ts(dawn.Add(-age))
	r.exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at,built_at,takes) VALUES(?,'manual','ready',1,'{}','human:alice',?,?,0)`, crate, at, at)
	r.exec(`INSERT INTO morning_crate_sides(crate_id,side_index,sha256,path,bytes,samples) VALUES(?,0,?,?,4,48000)`, crate, sha, rel)
	return rel
}

func (r *rig) exists(rel string) bool {
	_, err := r.data.Stat(rel)
	return err == nil
}

func sha(n int) string { return fmtSha(n) }

func fmtSha(n int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 64)
	for i := range b {
		b[i] = hex[(n+i)%16]
	}
	return string(b)
}

func TestMorning_SweepRemovesOldSides(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	old := r.side("old", sha(1), 8*24*time.Hour)
	fresh := r.side("fresh", sha(2), 6*24*time.Hour)
	n, err := r.svc.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if r.exists(old) || !r.exists(fresh) {
		t.Fatalf("old exists %v, fresh exists %v", r.exists(old), r.exists(fresh))
	}
	// the row is marked, never deleted
	if s := r.scalar(`SELECT COUNT(*) || '/' || COUNT(removed_at) FROM morning_crate_sides`); s != "2/1" {
		t.Fatalf("rows/removed = %s", s)
	}
	c, err := r.svc.Get(ctx, owner, "old")
	if err != nil || len(c.Sides) != 1 || !c.Sides[0].Removed || c.Sides[0].URL != nil || c.Sides[0].ExpiresAt != nil {
		t.Fatalf("old crate %+v %v", c, err)
	}
	if _, err := r.svc.Resolve(ctx, sha(1), "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("a removed side still resolves: %v", err)
	}
	// a second sweep is a no-op
	if n, err := r.svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("second sweep = %d, %v", n, err)
	}
	// the retention is a setting: keep 5 days, and the 6-day-old one goes too
	r.setting("keep_days", 5)
	if n, err := r.svc.Sweep(ctx); err != nil || n != 1 || r.exists(fresh) {
		t.Fatalf("keep 5: %d %v exists %v", n, err, r.exists(fresh))
	}
}

// TestMorning_SweepKeepsSharedSide: a file is shared by every crate with its sha and stays while any of them is young.
func TestMorning_SweepKeepsSharedSide(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	rel := r.side("old", sha(3), 10*24*time.Hour)
	at := ts(dawn.Add(-time.Hour))
	r.exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at,built_at,takes) VALUES('young','manual','ready',1,'{}','human:alice',?,?,0)`, at, at)
	r.exec(`INSERT INTO morning_crate_sides(crate_id,side_index,sha256,path,bytes,samples) VALUES('young',0,?,?,4,48000)`, sha(3), rel)
	if n, err := r.svc.Sweep(ctx); err != nil || n != 0 || !r.exists(rel) {
		t.Fatalf("sweep = %d, %v, exists %v", n, err, r.exists(rel))
	}
	if n := r.scalar(`SELECT COUNT(removed_at) FROM morning_crate_sides`); n != "0" {
		t.Fatalf("marked %s", n)
	}
	// once the young crate ages out, both go together
	r.clk.Advance(8 * 24 * time.Hour)
	if n, err := r.svc.Sweep(ctx); err != nil || n != 1 || r.exists(rel) {
		t.Fatalf("later sweep = %d, %v, exists %v", n, err, r.exists(rel))
	}
	if n := r.scalar(`SELECT COUNT(removed_at) FROM morning_crate_sides`); n != "2" {
		t.Fatalf("marked %s, want both rows", n)
	}
}

// TestMorning_SweepOrphans: an unreferenced file older than a day goes, a young one (a build in flight) stays, and a failed
// removal of a missing file is not an error.
func TestMorning_SweepOrphans(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if err := r.data.MkdirAll("derived/morning/aa", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"derived/morning/aa/orphan-old.flac", "derived/morning/aa/orphan-young.flac"} {
		if err := r.data.WriteFile(f, []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	old := dawn.Add(-48 * time.Hour)
	if err := os.Chtimes(r.dir+"/derived/morning/aa/orphan-old.flac", old, old); err != nil {
		t.Fatal(err)
	}
	r.clk.Set(time.Now()) // file mtimes are real; the service compares them with its clock
	n, err := r.svc.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if r.exists("derived/morning/aa/orphan-old.flac") || !r.exists("derived/morning/aa/orphan-young.flac") {
		t.Fatal("orphan handling wrong")
	}
	// a file other than under derived/morning is never touched
	if err := r.data.MkdirAll("derived/tapes", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.data.WriteFile("derived/tapes/keep.flac", []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(r.dir+"/derived/tapes/keep.flac", old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Sweep(ctx); err != nil || !r.exists("derived/tapes/keep.flac") {
		t.Fatalf("the sweep reached outside derived/morning: %v", err)
	}
}

func TestMorning_SweepMissingFileIsFine(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rel := r.side("old", sha(4), 9*24*time.Hour)
	if err := r.data.Remove(rel); err != nil {
		t.Fatal(err)
	}
	if n, err := r.svc.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if n := r.scalar(`SELECT COUNT(removed_at) FROM morning_crate_sides`); n != "1" {
		t.Fatalf("row not marked: %s", n)
	}
}

// TestMorning_ResolverOnlyMorningDir: only a live side answers; a master, an unknown sha, a wrong extension, a removed or missing
// file are all not found.
func TestMorning_ResolverOnlyMorningDir(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	rel := r.side("c1", sha(5), time.Hour)
	o, err := r.svc.Resolve(ctx, sha(5), "flac")
	if err != nil || o.RelPath != rel || o.ContentType != "audio/flac" || o.ETag != sha(5) || o.DownloadName != "" || o.ModTime.IsZero() {
		t.Fatalf("object %+v, %v", o, err)
	}
	var master string
	if err := r.st.R.QueryRow(`SELECT master_sha256 FROM tracks LIMIT 1`).Scan(&master); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string][2]string{"master": {master, "flac"}, "unknown": {sha(9), "flac"}, "ext": {sha(5), "mp3"}, "short": {"abc", "flac"}} {
		if _, err := r.svc.Resolve(ctx, tc[0], tc[1]); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := r.data.Remove(rel); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Resolve(ctx, sha(5), "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
	// the table refuses a path outside derived/morning, so no row can point a link at a master
	if _, err := r.st.W.Exec(`INSERT INTO morning_crate_sides(crate_id,side_index,sha256,path,bytes,samples) VALUES('c1',1,?,'masters/x.flac',1,1)`, sha(6)); err == nil {
		t.Error("a side row outside derived/morning was accepted")
	}
	if _, err := os.Stat(r.dir + "/" + rel); !errors.Is(err, fs.ErrNotExist) {
		t.Error("stat")
	}
}
