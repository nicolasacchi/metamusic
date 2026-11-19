package archtest

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// U1 C307.3: with one rating row per rater, a join on `ratings WHERE source = 'human'` fans out (COUNT and SUM inflate
// silently) and lets a Listener's or Editor's stars steer curation. The lint therefore targets the table, not the
// literal: SQL text naming `ratings` after FROM, JOIN, INTO or UPDATE is banned outside the packages and files that own
// the table. Readers go through the owner_ratings and critic_ratings views, or through store.ViewerRatingJoin, which
// pins one rater.
var ratingsTableRE = regexp.MustCompile("(?i)\\b(?:from|join|into|update)\\s+(?:main\\.)?[\"`\\[]?ratings\\b")

// ratingsTableAllowed are the only non-test locations (helper subtrees such as librarytest are exempt) that may name the
// ratings table in SQL, one reason each. Verdict tables (duel_verdicts, callback_verdicts) are out of scope.
var ratingsTableAllowed = map[string]string{
	"internal/judgments":            "owns the ratings table: Rate, HumanStars, orphan raters",
	"internal/library/judgments.go": "the per-rater read behind library.Ratings (every human rater, shaped by ShapeRating)",
	"internal/store/ratings_sql.go": "the viewer join helper: the one place a reader may join one rater's row",
	"internal/store/migrations":     "migration files",
}

func ratingsTableOffenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if helperSubtree(dir) {
			continue
		}
		allowed := false
		for a := range ratingsTableAllowed {
			allowed = allowed || path == a || inDir(path, a)
		}
		if allowed {
			continue
		}
		var code []string
		for _, line := range strings.Split(src, "\n") {
			c, _, _ := strings.Cut(line, "//")
			code = append(code, c)
		}
		// whole-file match, so "FROM\n ratings" split over two lines is caught too
		for _, m := range ratingsTableRE.FindAllString(strings.Join(code, "\n"), -1) {
			off = append(off, path+": "+strings.Join(strings.Fields(m), " "))
		}
	}
	sort.Strings(off)
	return off
}

func TestNoRawHumanRatingJoin(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	all := map[string]string{}
	for _, top := range []string{"internal", "cmd"} {
		for p, s := range goFiles(t, filepath.Join(root, top)) {
			all[top+"/"+p] = s
		}
	}
	if len(all) < 100 {
		t.Fatalf("only %d files scanned; the walk is broken", len(all))
	}
	if off := ratingsTableOffenders(all); len(off) > 0 {
		t.Fatalf("SQL reads the ratings table outside its owners; use owner_ratings, critic_ratings or store.ViewerRatingJoin (C307.3):\n%s", strings.Join(off, "\n"))
	}
	for p, why := range ratingsTableAllowed {
		if strings.TrimSpace(why) == "" {
			t.Errorf("allow-list entry %s has no reason", p)
		}
	}
}

func TestNoRawHumanRatingJoin_PatternCatches(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"internal/x/a.go":    "q := `SELECT stars FROM ratings WHERE source = 'human'`",
		"internal/x/b.go":    "q := `LEFT JOIN ratings hs ON hs.track_id = t.id AND hs.source = 'human'`",
		"internal/x/c.go":    "q := `INSERT INTO ratings(track_id) VALUES (?)`",
		"internal/x/d.go":    "q := `UPDATE ratings SET stars = 1`",
		"internal/x/e.go":    "q := `select 1 from\tRATINGS r`",
		"internal/x/f.go":    "q := `FROM main.ratings r`",
		"internal/x/g.go":    "q := \"JOIN `ratings` r\"",
		"cmd/metamusic/h.go": "q := `FROM ratings`",
		"internal/x/i.go":    "q := `SELECT 1 FROM\n\t\tratings r`",
	}
	if off := ratingsTableOffenders(bad); len(off) != len(bad) {
		t.Fatalf("want %d offenders, got %d: %v", len(bad), len(off), off)
	}
	ok := map[string]string{
		"internal/x/a.go":                "q := `SELECT stars FROM owner_ratings r JOIN critic_ratings c ON c.track_id = r.track_id`",
		"internal/x/b.go":                "// FROM ratings in a comment\nx := 1",
		"internal/x/c.go":                "n := `SELECT COUNT(*) FROM duel_verdicts WHERE source = 'human'`",
		"internal/judgments/ratings.go":  "q := `INSERT INTO ratings(track_id) VALUES (?)`",
		"internal/library/judgments.go":  "q := `SELECT rater FROM ratings WHERE track_id = ?`",
		"internal/store/ratings_sql.go":  "q := ` LEFT JOIN ratings x ON x.rater = ?`",
		"internal/store/migrations/x.go": "q := `FROM ratings`",
		"internal/librarytest/scene.go":  "q := `INSERT INTO ratings(track_id) VALUES (?)`",
		"internal/servertest/seed.go":    "q := `UPDATE ratings SET stars = 1`",
		"internal/x/d.go":                "t := \"ratings\" // the table name alone is not a read",
	}
	if off := ratingsTableOffenders(ok); len(off) != 0 {
		t.Fatalf("false positives: %v", off)
	}
}

// ownerEventsRE finds a hard-coded "human or device" listening filter. After U1 a Listener's or Editor's plays must
// not steer the Owner's heat, completion, last_played or unheard (C306.7), so readers take the filter from
// store.OwnerEventsSQL instead of spelling it.
var ownerEventsRE = regexp.MustCompile(`(?i)principal_kind\s+IN\s*\(\s*'human'`)

func ownerEventsOffenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if helperSubtree(dir) || path == "internal/store/listen_sql.go" {
			continue
		}
		for _, line := range strings.Split(src, "\n") {
			code, _, _ := strings.Cut(line, "//")
			if ownerEventsRE.MatchString(code) {
				off = append(off, path+": "+strings.TrimSpace(code))
			}
		}
	}
	sort.Strings(off)
	return off
}

func TestNoRawListenEventOwnerFilter(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	all := map[string]string{}
	for _, top := range []string{"internal", "cmd"} {
		for p, s := range goFiles(t, filepath.Join(root, top)) {
			all[top+"/"+p] = s
		}
	}
	if off := ownerEventsOffenders(all); len(off) > 0 {
		t.Fatalf("listen_events filtered by principal_kind outside store.OwnerEventsSQL (C306.7):\n%s", strings.Join(off, "\n"))
	}
	bad := map[string]string{"internal/x/a.go": "WHERE e.principal_kind IN ('human','device')", "internal/x/b.go": "principal_kind in ( 'human', 'device')"}
	if off := ownerEventsOffenders(bad); len(off) != 2 {
		t.Fatalf("want 2 offenders, got %v", off)
	}
}
