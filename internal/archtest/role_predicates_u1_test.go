package archtest

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// U1 C303: privileges follow the role, never the principal kind. A rule written as "not an agent, therefore the
// operator" or "is a human, therefore the Owner" silently hands Owner powers to an Editor or a Listener, so the old
// patterns are banned outside internal/auth (which defines the predicates) and internal/view. Code uses IsPerson,
// IsOwner, IsOwnerSide, CanSeePrivate, IsAgent and IsHuman instead.
var rolePredicateBans = regexp.MustCompile(
	`\bActsAsHuman\b|\.Kind\s*(==|!=)\s*(auth\.)?Kind(Human|Agent)\b|\bcase\s+([^:\n]*[\s,])?(auth\.)?Kind(Human|Agent)\b`)

// rolePredicateAllowed names the sites that may keep a raw kind check, one reason each. It is empty: every agent-only
// rule uses IsAgent. A new entry needs a reason that says why no predicate fits.
var rolePredicateAllowed = map[string]string{}

func rolePredicateOffenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		if inDir(path, "internal/auth") || inDir(path, "internal/view") {
			continue
		}
		if _, ok := rolePredicateAllowed[path]; ok {
			continue
		}
		for _, line := range strings.Split(src, "\n") {
			code, _, _ := strings.Cut(line, "//")
			if rolePredicateBans.MatchString(code) {
				off = append(off, path+": "+strings.TrimSpace(code))
			}
		}
	}
	sort.Strings(off)
	return off
}

func TestRolePredicates_NoRawKindChecks(t *testing.T) {
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
	if off := rolePredicateOffenders(all); len(off) > 0 {
		t.Fatalf("raw principal-kind checks outside internal/auth (use the role predicates, C303):\n%s", strings.Join(off, "\n"))
	}
	for p, why := range rolePredicateAllowed {
		if strings.TrimSpace(why) == "" {
			t.Errorf("allow-list entry %s has no reason", p)
		}
	}
}

func TestRolePredicates_BanPatternsCatch(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"internal/x/a.go": "if p.ActsAsHuman() {}",
		"internal/x/b.go": "if p.Kind != auth.KindAgent {}",
		"internal/x/c.go": "if viewer.Kind == auth.KindHuman {}",
		"internal/x/d.go": "switch {\ncase auth.KindDevice, auth.KindAgent:\n}",
		"internal/x/e.go": "if p.Kind == KindAgent {}",
		"internal/x/f.go": "if q.Kind\t!=  auth.KindHuman {}",
	}
	if off := rolePredicateOffenders(bad); len(off) != 6 {
		t.Fatalf("want 6 offenders, got %d: %v", len(off), off)
	}
	ok := map[string]string{
		"internal/auth/p.go": "return p.Kind == KindHuman",
		"internal/x/a.go":    "if p.IsAgent() || p.IsHuman() {} // p.Kind == auth.KindAgent in a comment",
		"internal/x/b.go":    "if p.Kind == auth.KindDevice {}",
		"internal/x/c.go":    `kind := string(auth.KindHuman)`,
	}
	if off := rolePredicateOffenders(ok); len(off) != 0 {
		t.Fatalf("false positives: %v", off)
	}
}
