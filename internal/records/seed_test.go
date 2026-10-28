package records_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

func TestCoverSeed_Golden(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		id      string
		rerolls int
		want    int64
	}{
		{"01900000-0000-7000-8000-000000000001", 0, 8386528864446139},
		{"01900000-0000-7000-8000-000000000001", 1, 3820461697493548},
		{"01900000-0000-7000-8000-0000000000ff", 0, 1500560678665390},
		{"018f2c3a-1b2c-7d4e-8f50-123456789abc", 7, 1464121087948865},
	} {
		if got := records.CoverSeed(c.id, c.rerolls); got != c.want {
			t.Errorf("CoverSeed(%q, %d) = %d, want %d", c.id, c.rerolls, got, c.want)
		}
	}
}

func TestCoverSeed_Properties(t *testing.T) {
	t.Parallel()
	const id = "01900000-0000-7000-8000-000000000001"
	if records.CoverSeed(id, 0) == records.CoverSeed(id, 1) {
		t.Error("a re-roll must change the seed")
	}
	again := records.CoverSeed(id, 0)
	if first := records.CoverSeed(id, 0); first != again {
		t.Errorf("the seed is deterministic: %d then %d", first, again)
	}
	seen := map[int64]bool{}
	for i := 0; i < 2000; i++ {
		s := records.CoverSeed(id, i)
		if s < 0 || s > 1<<53-1 {
			t.Fatalf("CoverSeed(%d) = %d is outside [0, 2^53-1]", i, s)
		}
		seen[s] = true
	}
	if len(seen) != 2000 {
		t.Errorf("%d distinct seeds in 2000 re-rolls", len(seen))
	}
}

var enumRE = regexp.MustCompile(`(?s)kind\s+TEXT NOT NULL CHECK \(kind IN \((.*?)\)\)`)
var reasonRE = regexp.MustCompile(`(?s)reason\s+TEXT CHECK \(reason IS NULL OR reason IN\s*\((.*?)\)\)`)

func sqlList(t *testing.T, re *regexp.Regexp, sql string) []string {
	t.Helper()
	m := re.FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("pattern %v not found in the migration", re)
	}
	var out []string
	for _, f := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, f[1])
	}
	return out
}

// TestKinds_MatchSchema: the Go lists of change kinds and reasons equal the CHECK lists of 0010_curation.sql, in order.
func TestKinds_MatchSchema(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../store/migrations/0010_curation.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	if got, want := strings.Join(records.Kinds, ","), strings.Join(sqlList(t, enumRE, sql), ","); got != want {
		t.Errorf("Kinds = %s\nschema = %s", got, want)
	}
	if got, want := strings.Join(records.Reasons, ","), strings.Join(sqlList(t, reasonRE, sql), ","); got != want {
		t.Errorf("Reasons = %s\nschema = %s", got, want)
	}
	for _, k := range records.SyncerKinds {
		found := false
		for _, a := range records.Kinds {
			found = found || a == k
		}
		if !found {
			t.Errorf("syncer kind %q is not a kind", k)
		}
	}
	if len(records.SyncerKinds) != 4 {
		t.Errorf("syncer kinds = %v, want the four of C119.2", records.SyncerKinds)
	}
}

func TestParams_Constants(t *testing.T) {
	t.Parallel()
	if records.MaxSlots != 60 || records.MaxEditOps != 50 || records.MaxProposalNote != 500 || records.MaxChangesPage != 200 ||
		records.SyncBatch != 200 || records.CoverParts != 12 || records.SyncRetries != 5 || records.SyncPoll.Seconds() != 1 {
		t.Error("a C118 constant changed")
	}
	if records.MaxTitle != 120 || records.MaxSubtitle != 200 || records.MaxLinerNote != 4000 {
		t.Error("a C119 length bound changed")
	}
}
