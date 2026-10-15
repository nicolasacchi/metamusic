package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// museSQLAllowlist are the only locations of non-test Go that may name the muses tables in SQL text (C99.2).
// songview reads Muse text through muses.Service.Ref/Get, so it needs no entry.
var museSQLAllowlist = []string{"internal/muses", "internal/board", "internal/store/migrations"}

var (
	museTableRE = regexp.MustCompile(`(?i)\b(?:from|join|into|update|table)\s+muses(?:_fts)?\b|\bmuses_fts\b`)
	writeRE     = regexp.MustCompile(`(?i)\b(?:insert\s+(?:or\s+\w+\s+)?into|update|delete\s+from)\s+([A-Za-z_][A-Za-z0-9_]*)`)
)

// writeOwners is C88.2: the packages that may write each L1 table (non-test Go; helper subtrees are exempt).
var writeOwners = map[string][]string{
	"muses":                   {"internal/muses"},
	"muses_fts":               {"internal/muses"},
	"songs":                   {"internal/songs", "internal/muses"}, // muses.AssignSongs writes songs.muse_id/muse_source (C89.1)
	"song_roots":              {"internal/songs"},
	"song_picks":              {"internal/picks"},
	"pick_events":             {"internal/picks"},
	"pick_proposals":          {"internal/picks"},
	"sessions":                {"internal/sessions"},
	"session_members":         {"internal/sessions"},
	"sound_taxonomy_versions": {"internal/sounds"},
	"sound_terms":             {"internal/sounds"},
	"take_sounds":             {"internal/sounds"},
	"song_sounds":             {"internal/sounds"},
	"sound_state":             {"internal/sounds"},
	"song_unmapped_terms":     {"internal/sounds"},
	"provenance_dirty":        {"internal/provenance"},
	"provenance_lease":        {"internal/provenance"},
}

// goFiles lists the non-test .go files below root (slash-separated paths relative to root).
func goFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func inDir(path, dir string) bool { return strings.HasPrefix(path, dir+"/") }

func museSQLOffenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		ok := false
		for _, a := range museSQLAllowlist {
			ok = ok || inDir(path, a)
		}
		if !ok && museTableRE.MatchString(src) {
			off = append(off, path)
		}
	}
	return off
}

func writeOwnerOffenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")+1]
		if helperSubtree(strings.TrimSuffix(dir, "/")) || inDir(path, "internal/store/migrations") {
			continue
		}
		for _, m := range writeRE.FindAllStringSubmatch(src, -1) {
			owners, owned := writeOwners[strings.ToLower(m[1])]
			if !owned {
				continue
			}
			allowed := false
			for _, o := range owners {
				allowed = allowed || inDir(path, o)
			}
			if !allowed {
				off = append(off, path+": "+m[1])
			}
		}
	}
	return off
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSQL_MuseTables_Allowlist(t *testing.T) {
	t.Parallel()
	if got := strings.Join(museSQLAllowlist, ","); got != "internal/muses,internal/board,internal/store/migrations" {
		t.Fatalf("the allowlist must be exactly muses, board and the migrations: %s", got)
	}
	files := goFiles(t, filepath.Join(repoRoot(t), "internal"))
	cmd := goFiles(t, filepath.Join(repoRoot(t), "cmd"))
	all := map[string]string{}
	for p, s := range files {
		all["internal/"+p] = s
	}
	for p, s := range cmd {
		all["cmd/"+p] = s
	}
	if len(all) < 100 {
		t.Fatalf("scanned only %d files", len(all))
	}
	if off := museSQLOffenders(all); len(off) > 0 {
		t.Errorf("non-test Go outside the Muse allowlist names the muses tables in SQL: %v", off)
	}
	// the checker bites
	plant := map[string]string{
		"internal/x/a.go":     "var q = `SELECT title FROM muses WHERE id = ?`",
		"internal/x/b.go":     "var q = `SELECT 1 FROM muses_fts`",
		"internal/muses/c.go": "var q = `SELECT title FROM muses`",
		"internal/board/d.go": "var q = `SELECT title FROM muses`",
		"internal/songs/e.go": "// the muses package owns Muse text",
		"internal/songs/f.go": "var q = `SELECT muse_id FROM songs`",
		"internal/y/g.go":     "var q = `SELECT * FROM songs JOIN muses m ON m.id = songs.muse_id`",
	}
	off := museSQLOffenders(plant)
	if len(off) != 3 {
		t.Errorf("planted offenders: %v, want a.go, b.go and g.go", off)
	}
}

func TestSQL_WriteOwners_L1(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	all := map[string]string{}
	for p, s := range goFiles(t, filepath.Join(root, "internal")) {
		all["internal/"+p] = s
	}
	for p, s := range goFiles(t, filepath.Join(root, "cmd")) {
		all["cmd/"+p] = s
	}
	if off := writeOwnerOffenders(all); len(off) > 0 {
		t.Errorf("a non-owner package writes an L1 table (C88.2): %v", off)
	}
	plant := map[string]string{
		"internal/api/x.go":          "var q = `INSERT INTO songs(id) VALUES (?)`",
		"internal/songs/ok.go":       "var q = `UPDATE songs SET title = ?`",
		"internal/muses/ok.go":       "var q = `UPDATE songs SET muse_id = ?`",
		"internal/library/y.go":      "var q = `DELETE FROM provenance_dirty`",
		"internal/provenance/ok.go":  "var q = `DELETE FROM provenance_dirty WHERE kind = ?`",
		"internal/picks/z.go":        "var q = `INSERT OR IGNORE INTO session_members(a) VALUES (1)`",
		"internal/libtest/helper.go": "var q = `INSERT INTO songs(id) VALUES (?)`",
	}
	off := writeOwnerOffenders(plant)
	if len(off) != 3 {
		t.Errorf("planted offenders: %v, want x.go, y.go and z.go", off)
	}
}
