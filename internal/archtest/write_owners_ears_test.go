package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// writeOwnersEars is the owner table of the M5b Ears tables (C349.5): the two tables of human judgement are written only by
// internal/diction, and the link of a recovered draft to its reference only by internal/drafts (the same transaction as the draft).
var writeOwnersEars = map[string][]string{
	"diction_marks":     {"internal/diction"},
	"lexicon_overrides": {"internal/diction"},
	"draft_recoveries":  {"internal/drafts"},
}

// dictionRules: the lab reads Tier B rows and serves them; it never reaches the job, spend or provider layers (a listening check
// creates no GPU work, D-EARS-9) nor ACE-Step (D-EARS-OP-2).
var dictionRules = []rule{
	{from: []string{"internal/diction"},
		deny: []string{"internal/jobs", "internal/generation", "internal/spend", "internal/provider", "internal/engine/acestep", "internal/api", "internal/app"},
		why:  "internal/diction sits below api and app and never reaches jobs, generation, spend or a provider (C330, D-EARS-9)"},
}

// earsOffenders returns one message per write of an Ears-owned table outside its owners (slash paths relative to the repo root).
func earsOffenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if helperSubtree(dir) || inDir(path, "internal/store/migrations") {
			continue
		}
		for _, m := range writeRE.FindAllStringSubmatch(stripLineComments(src), -1) {
			owners, owned := writeOwnersEars[strings.ToLower(m[1])]
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

func TestWriteOwners_Ears(t *testing.T) {
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
	if off := earsOffenders(all); len(off) > 0 {
		t.Errorf("C349.5 violated: %v", off)
	}
	for _, f := range []string{"internal/diction/marks.go", "internal/drafts/recovered.go"} {
		if _, ok := all[f]; !ok {
			t.Errorf("%s is not scanned; the owner table is stale", f)
		}
	}
	// the checker bites
	off := earsOffenders(map[string]string{
		"internal/api/a.go":                  "var q = `INSERT INTO diction_marks(track_id) VALUES (?)`",
		"internal/ears/b.go":                 "var q = `UPDATE lexicon_overrides SET status = 'ignored'`",
		"internal/diction/ok.go":             "var q = `INSERT INTO diction_marks(track_id) VALUES (?)` + `UPDATE lexicon_overrides SET note = NULL`",
		"internal/diction/diction_test/x.go": "var q = `INSERT INTO diction_marks(track_id) VALUES (?)`",
		"internal/ears/c.go":                 "// INSERT INTO diction_marks is documented here\nvar q = `SELECT * FROM diction_marks`",
		"internal/api/d.go":                  "var q = `INSERT INTO draft_recoveries(draft_id) VALUES (?)`",
		"internal/drafts/ok.go":              "var q = `INSERT INTO draft_recoveries(draft_id) VALUES (?)`",
		"internal/ears/e.go":                 "var q = `SELECT track_id FROM draft_recoveries`",
	})
	if len(off) != 3 {
		t.Errorf("planted offenders = %v, want exactly internal/api/a.go, internal/ears/b.go and internal/api/d.go", off)
	}
}

func TestImportGraph_Diction(t *testing.T) {
	t.Parallel()
	g := loadGraph(t)
	if _, ok := g["internal/diction"]; !ok {
		t.Fatal("internal/diction is not listed")
	}
	msgs, checked := violations(g, dictionRules)
	for _, m := range msgs {
		t.Error(m)
	}
	if checked < 1 {
		t.Fatal("no constrained package found; the table is stale")
	}
}
