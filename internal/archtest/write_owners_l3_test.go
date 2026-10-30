package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// writeOwnersL3 is C134.2: the packages that may write each L3 table (non-test Go; helper subtrees are exempt). The tables of
// earlier plans keep their owners (L1's `song_sounds`, `sound_*` and `provenance_dirty` tables stay with sounds and provenance,
// which covers song_sounds.ord, accepted proposals and the queue; L2's record_changes stays with records).
var writeOwnersL3 = map[string][]string{
	"take_features":      {"internal/features"},
	"song_features":      {"internal/features"},
	"record_sequences":   {"internal/records"},
	"taxonomy_proposals": {"internal/sounds"},
}

// l3Offenders returns one message per violation of C134.2 in files (slash paths relative to the repo root).
func l3Offenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if helperSubtree(dir) || inDir(path, "internal/store/migrations") {
			continue
		}
		for _, m := range writeRE.FindAllStringSubmatch(src, -1) {
			owners, owned := writeOwnersL3[strings.ToLower(m[1])]
			if !owned {
				continue
			}
			allowed := false
			for _, o := range owners {
				allowed = allowed || inDir(path, o)
			}
			if !allowed {
				off = append(off, path+": writes "+m[1])
			}
		}
	}
	return off
}

// TestSQL_WriteOwners_L3 is the scaffold of the owner table of C134.2: the four new tables have one writing package each. The
// owners do not exist yet (the lanes land them); the guard is already live for everyone else.
func TestSQL_WriteOwners_L3(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	all := map[string]string{}
	for p, s := range goFiles(t, filepath.Join(root, "internal")) {
		all["internal/"+p] = s
	}
	for p, s := range goFiles(t, filepath.Join(root, "cmd")) {
		all["cmd/"+p] = s
	}
	if len(all) < 100 {
		t.Fatalf("scanned only %d files", len(all))
	}
	if off := l3Offenders(all); len(off) > 0 {
		t.Errorf("C134.2 violated: %v", off)
	}
	// the earlier owner tables are unchanged by L3: song_sounds (and so its ord column) belongs to sounds alone
	if got := strings.Join(writeOwners["song_sounds"], ","); got != "internal/sounds" {
		t.Errorf("song_sounds owners = %s", got)
	}
	if got := strings.Join(writeOwners["provenance_dirty"], ","); got != "internal/provenance" {
		t.Errorf("provenance_dirty owners = %s: no L3 Go code writes the queue", got)
	}
	// the checker bites
	plant := map[string]string{
		"internal/api/a.go":                 "var q = `INSERT INTO take_features(track_id) VALUES (?)`",
		"internal/features/ok.go":           "var q = `DELETE FROM take_features WHERE track_id = ?` + `INSERT INTO song_features(song_id) VALUES (?)`",
		"internal/sounds/b.go":              "var q = `INSERT INTO song_features(song_id) VALUES (?)`",
		"internal/sequence/c.go":            "var q = `INSERT INTO record_sequences(id) VALUES (?)`",
		"internal/records/ok.go":            "var q = `INSERT INTO record_sequences(id) VALUES (?)`",
		"internal/atlas/d.go":               "var q = `UPDATE taxonomy_proposals SET state = 'open'`",
		"internal/sounds/ok.go":             "var q = `UPDATE taxonomy_proposals SET state = 'accepted'`",
		"internal/recordview/e.go":          "var q = `SELECT * FROM record_sequences` + `SELECT * FROM taxonomy_proposals`",
		"internal/library/librarytest/f.go": "var q = `INSERT INTO take_features(track_id) VALUES (?)`",
		"internal/store/migrations/x":       "var q = `INSERT INTO take_features(track_id) VALUES (?)`",
	}
	off := l3Offenders(plant)
	want := map[string]bool{"internal/api/a.go": true, "internal/sounds/b.go": true, "internal/sequence/c.go": true, "internal/atlas/d.go": true}
	got := map[string]bool{}
	for _, o := range off {
		got[o[:strings.Index(o, ":")]] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("planted offender %s was not found (found %v)", p, off)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("%s was reported but is allowed: %v", p, off)
		}
	}
}
