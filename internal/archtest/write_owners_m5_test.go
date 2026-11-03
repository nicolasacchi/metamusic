package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// writeOwnersM5 is C250's owner table: the packages that may write each M5 table (non-test Go; helper subtrees and the
// migrations are exempt). Comment state and rows stay owned by judgments (callbacks calls its exported ...Tx functions); the
// new duels columns belong to duels, which already writes the table.
var writeOwnersM5 = map[string][]string{
	"patches":           {"internal/songpr"},
	"patch_comments":    {"internal/songpr"},
	"callbacks":         {"internal/callbacks"},
	"callback_verdicts": {"internal/callbacks"},
	"voice_notes":       {"internal/voice"},
}

// stripLineComments blanks whole-line // comments: "Update patches a Muse" is prose, not SQL.
func stripLineComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// m5Offenders returns one message per violation in files (slash paths relative to the repo root).
func m5Offenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if helperSubtree(dir) || inDir(path, "internal/store/migrations") {
			continue
		}
		for _, m := range writeRE.FindAllStringSubmatch(stripLineComments(src), -1) {
			owners, owned := writeOwnersM5[strings.ToLower(m[1])]
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

// TestSQL_WriteOwners_M5: the five new tables have one writing package each. The owners may not exist yet (the lanes land
// them); the guard is already live for everyone else.
func TestSQL_WriteOwners_M5(t *testing.T) {
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
	if off := m5Offenders(all); len(off) > 0 {
		t.Errorf("C250 violated: %v", off)
	}
	plant := map[string]string{
		"internal/api/a.go":                "var q = `INSERT INTO patches(id) VALUES (?)`",
		"internal/songpr/ok.go":            "var q = `INSERT INTO patches(id) VALUES (?)` + `INSERT INTO patch_comments(patch_id) VALUES (?)`",
		"internal/callbacks/b.go":          "var q = `INSERT INTO patch_comments(patch_id) VALUES (?)`",
		"internal/callbacks/ok.go":         "var q = `INSERT INTO callbacks(id) VALUES (?)` + `INSERT INTO callback_verdicts(id) VALUES (?)`",
		"internal/judgments/c.go":          "var q = `INSERT INTO callback_verdicts(id) VALUES (?)`",
		"internal/voice/ok.go":             "var q = `INSERT INTO voice_notes(id) VALUES (?)`",
		"internal/library/d.go":            "var q = `DELETE FROM voice_notes WHERE id = ?`",
		"internal/songpr/readonly.go":      "var q = `SELECT * FROM callbacks` + `SELECT * FROM voice_notes`",
		"internal/songpr/librarytest/f.go": "var q = `INSERT INTO callbacks(id) VALUES (?)`",
		"internal/store/migrations/x":      "var q = `INSERT INTO patches(id) VALUES (?)`",
	}
	want := map[string]bool{"internal/api/a.go": true, "internal/callbacks/b.go": true, "internal/judgments/c.go": true, "internal/library/d.go": true}
	got := map[string]bool{}
	for _, o := range m5Offenders(plant) {
		got[o[:strings.Index(o, ":")]] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("planted offender %s was not found", p)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("%s was reported but is allowed", p)
		}
	}
}
