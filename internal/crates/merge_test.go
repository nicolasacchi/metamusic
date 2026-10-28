package crates

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// mergeCases are two different values of every key of the vocabulary: two scalars conflict (naming the key), two lists
// intersect to nothing, equal values merge. Together with TestVocab_Tables it makes the merge total over the vocabulary.
var mergeCases = map[Subject]map[string][2]string{
	SubjectSong: {
		"muse_id": {`"a"`, `"b"`}, "angle": {`"a"`, `"b"`}, "session_id": {`"a"`, `"b"`}, "q": {`"a"`, `"b"`},
		"queued": {`true`, `false`}, "tempo_mismatch": {`true`, `false`}, "has_pick": {`true`, `false`}, "has_open_proposals": {`true`, `false`},
		"pick_decided": {`true`, `false`}, "unheard": {`true`, `false`}, "unrated": {`true`, `false`}, "lossy": {`true`, `false`},
		"recipe_known": {`true`, `false`}, "in_record": {`true`, `false`},
		"min_duration_s": {`1`, `2`}, "max_duration_s": {`1`, `2`}, "min_stars": {`1`, `2`},
		"state": {`["draft"]`, `["rendered"]`}, "sound": {`["genre_family:a"]`, `["genre_family:b"]`}, "tag": {`["a"]`, `["b"]`},
	},
	SubjectTake: {
		"q": {`"a"`, `"b"`}, "recorded_from": {`"2026-01-01"`, `"2026-01-02"`}, "recorded_to": {`"2026-01-01"`, `"2026-01-02"`}, "session_id": {`"a"`, `"b"`},
		"has_score": {`true`, `false`}, "truncated": {`true`, `false`}, "has_open_comments": {`true`, `false`}, "recipe_known": {`true`, `false`},
		"lossy": {`true`, `false`}, "is_pick": {`true`, `false`}, "unheard": {`true`, `false`}, "in_record": {`true`, `false`},
		"min_duration_s": {`1`, `2`}, "max_duration_s": {`1`, `2`}, "min_stars": {`1`, `2`},
		"status": {`["draft"]`, `["final"]`}, "style": {`["a"]`, `["b"]`}, "tag": {`["a"]`, `["b"]`}, "sound": {`["genre_family:a"]`, `["genre_family:b"]`},
	},
}

func parse(t *testing.T, s Subject, key, v string) Filter {
	t.Helper()
	f, err := ParseFilter(s, json.RawMessage(`{"`+key+`":`+v+`}`))
	if err != nil {
		t.Fatalf("%s.%s=%s: %v", s, key, v, err)
	}
	return f
}

func TestMerge_EveryVocabularyKey(t *testing.T) {
	t.Parallel()
	for subject, cases := range mergeCases {
		keys := Keys(string(subject))
		if len(cases) != len(keys)-boolOnlyKeys(subject) {
			t.Errorf("%s: %d merge cases for %d keys", subject, len(cases), len(keys))
		}
		for _, k := range keys {
			c, ok := cases[k.Name]
			if !ok {
				continue // no_muse, unrated
			}
			a, b := parse(t, subject, k.Name, c[0]), parse(t, subject, k.Name, c[1])
			var errAB, errAA error
			var noneAB bool
			if subject == SubjectSong {
				_, errAA = mergeSongs(a.Songs(), a.Songs())
				m, e := mergeSongs(a.Songs(), b.Songs())
				errAB, noneAB = e, m.None
			} else {
				_, errAA = mergeTracks(a.Tracks(), a.Tracks())
				m, e := mergeTracks(a.Tracks(), b.Tracks())
				errAB, noneAB = e, m.None
			}
			if errAA != nil {
				t.Errorf("%s.%s: equal values must merge: %v", subject, k.Name, errAA)
			}
			if k.Kind == KindStrings {
				if errAB != nil || !noneAB {
					t.Errorf("%s.%s: disjoint lists must intersect to nothing: %v none=%v", subject, k.Name, errAB, noneAB)
				}
				continue
			}
			var pe *problem.Error
			if !errors.As(errAB, &pe) || pe.Code != "FILTER_CONFLICT" || pe.Details["field"] != k.Name {
				// min_stars with unrated: tracks refuse the combination instead
				t.Errorf("%s.%s: %v, want FILTER_CONFLICT naming the key", subject, k.Name, errAB)
			}
		}
	}
}

// boolOnlyKeys counts the keys whose false is "unset", which merge by OR instead of conflicting: no_muse on Songs, unrated on
// Takes.
func boolOnlyKeys(Subject) int { return 1 }

func TestMerge_ExclusiveStars(t *testing.T) {
	t.Parallel()
	a := parse(t, SubjectTake, "min_stars", `3`)
	b := parse(t, SubjectTake, "unrated", `true`)
	if _, err := mergeTracks(a.Tracks(), b.Tracks()); err == nil {
		t.Error("min_stars from one side and unrated from the other are exclusive")
	}
	sa := parse(t, SubjectSong, "min_stars", `3`)
	sb := parse(t, SubjectSong, "unrated", `true`)
	if _, err := mergeSongs(sa.Songs(), sb.Songs()); err == nil {
		t.Error("min_stars from one side and unrated from the other are exclusive for Songs")
	}
	nm := parse(t, SubjectSong, "no_muse", `true`)
	if out, err := mergeSongs(nm.Songs(), sa.Songs()); err != nil || !out.NoMuse || out.MinStars == nil {
		t.Errorf("no_muse merges by OR: %+v %v", out, err)
	}
}
