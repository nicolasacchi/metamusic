package crates_test

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/crates"
)

func names(ks []crates.Key) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Name
	}
	return out
}

// TestVocab_Tables: the C125.2 key tables as data: no duplicates, every key typed, the non-saved set disjoint from both.
func TestVocab_Tables(t *testing.T) {
	t.Parallel()
	for subject, ks := range map[string][]crates.Key{"song": crates.SongKeys, "take": crates.TakeKeys} {
		seen := map[string]bool{}
		for _, k := range ks {
			if seen[k.Name] {
				t.Errorf("%s: duplicate key %q", subject, k.Name)
			}
			seen[k.Name] = true
			if k.Name == "" || k.Kind == "" || strings.ToLower(k.Name) != k.Name {
				t.Errorf("%s: key %+v is not a lower-case typed name", subject, k)
			}
			if crates.IsNonSaved(k.Name) {
				t.Errorf("%s: %q is both a saved key and a non-saved parameter", subject, k.Name)
			}
			if k.Kind == crates.KindStrings && k.MaxItems <= 0 && len(k.Enum) == 0 && k.Name != "state" && k.Name != "status" {
				t.Errorf("%s: list key %q has no item bound", subject, k.Name)
			}
			if got, ok := crates.Lookup(subject, k.Name); !ok || got.Name != k.Name {
				t.Errorf("Lookup(%s, %s) = %v %v", subject, k.Name, got, ok)
			}
		}
		if !slices.Equal(names(crates.Keys(subject)), names(ks)) {
			t.Errorf("Keys(%q) is not the table", subject)
		}
		if _, ok := crates.Lookup(subject, "crate_id"); ok {
			t.Errorf("%s: crate_id is not a saved key (no nesting)", subject)
		}
	}
	if len(crates.SongKeys) != 21 || len(crates.TakeKeys) != 20 {
		t.Errorf("%d song keys and %d take keys, want 21 and 20", len(crates.SongKeys), len(crates.TakeKeys))
	}
	want := []string{"crate_id", "cursor", "draft_id", "include_archived", "include_hidden", "limit", "root_track_id", "song_id", "sort", "track_id"}
	got := slices.Clone(crates.NonSaved)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("non-saved set = %v, want %v", got, want)
	}
	if crates.Keys("muse") != nil || crates.Keys("") != nil {
		t.Error("an unknown subject has no keys")
	}
	if _, ok := crates.Lookup("muse", "q"); ok {
		t.Error("Lookup on an unknown subject")
	}
}

// TestVocab_NewKeys pins which keys L2 adds to the shipped list ops (D-L2-13) and that has_pick keeps its meaning.
func TestVocab_NewKeys(t *testing.T) {
	t.Parallel()
	added := func(ks []crates.Key) (out []string) {
		for _, k := range ks {
			if k.New {
				out = append(out, k.Name)
			}
		}
		sort.Strings(out)
		return out
	}
	if got, want := added(crates.SongKeys), []string{"in_record", "lossy", "max_duration_s", "min_duration_s", "min_stars", "pick_decided", "recipe_known", "tag", "unheard", "unrated"}; !slices.Equal(got, want) {
		t.Errorf("new song keys = %v", got)
	}
	if got, want := added(crates.TakeKeys), []string{"in_record", "sound", "unheard"}; !slices.Equal(got, want) {
		t.Errorf("new take keys = %v", got)
	}
	if k, ok := crates.Lookup("song", "has_pick"); !ok || k.New || k.Kind != crates.KindBool {
		t.Errorf("has_pick must stay the shipped boolean: %+v", k)
	}
	if k, ok := crates.Lookup("song", "pick_decided"); !ok || !k.New || k.Kind != crates.KindBool {
		t.Errorf("pick_decided: %+v", k)
	}
	// the concept's example filter uses only vocabulary keys
	for _, key := range []string{"sound", "unheard", "min_duration_s", "pick_decided"} {
		if _, ok := crates.Lookup("song", key); !ok {
			t.Errorf("the worked example needs %q", key)
		}
	}
}

var queryTag = regexp.MustCompile(`query:"([a-z_]+)`)

// opParams returns the query parameter names inside the named struct types of an api source file.
func opParams(t *testing.T, file string, structs ...string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	var out []string
	for _, s := range structs {
		i := strings.Index(src, "type "+s+" struct {")
		if i < 0 {
			t.Fatalf("%s: type %s not found", file, s)
		}
		body := src[i:]
		body = body[:strings.Index(body, "\n}")]
		for _, m := range queryTag.FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s: no query parameters found", file)
	}
	return out
}

// TestVocab_MatchesShippedOps is the early form of the drift guard (the reflection form is lane K's): every query parameter
// of list_songs and search_tracks is either a vocabulary key or in the non-saved set, and every key that is not marked New
// is a parameter the shipped op really has. It holds before and after lane K adds the new predicates to the ops.
func TestVocab_MatchesShippedOps(t *testing.T) {
	t.Parallel()
	for subject, c := range map[string]struct {
		params []string
		keys   []crates.Key
	}{
		"song": {opParams(t, "../api/types_l1.go", "SongFilterIn", "listSongsIn"), crates.SongKeys},
		"take": {opParams(t, "../api/types_m1.go", "searchTracksIn"), crates.TakeKeys},
	} {
		inVocab := map[string]bool{}
		for _, k := range c.keys {
			inVocab[k.Name] = true
		}
		inOp := map[string]bool{}
		for _, p := range c.params {
			inOp[p] = true
			if !inVocab[p] && !crates.IsNonSaved(p) {
				t.Errorf("%s: op parameter %q is neither a vocabulary key nor non-saved", subject, p)
			}
		}
		for _, k := range c.keys {
			if !k.New && !inOp[k.Name] {
				t.Errorf("%s: shipped key %q is not a parameter of the op", subject, k.Name)
			}
		}
		for p := range inOp {
			if crates.IsNonSaved(p) && inVocab[p] {
				t.Errorf("%s: %q is in both sets", subject, p)
			}
		}
	}
}

func TestParams_Constants(t *testing.T) {
	t.Parallel()
	if crates.MaxManualItems != 5000 || crates.MaxEditBatch != 200 || crates.MaxFilterBytes != 4096 || crates.MaxName != 80 || crates.MaxDescription != 500 {
		t.Error("a C118 constant changed")
	}
}
