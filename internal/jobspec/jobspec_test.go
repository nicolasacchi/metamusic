package jobspec

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var sha = strings.Repeat("ab", 32)

// TestJobspec_StrictParse: an empty document is the zero spec; unknown fields, trailing data, a wrong type and every
// out-of-range value are refused, and what is accepted round-trips canonically.
func TestJobspec_StrictParse(t *testing.T) {
	t.Parallel()
	good := `{"inputs":{"src_audio":{"track_id":"t1","master_sha256":"` + sha + `","bytes":1234}},` +
		`"ears":{"lang":"it","budget_s":120,"stages":["separate","asr"],"keep_vocals":true},` +
		`"plan":{"seeds":[1,2,3],"seconds_per_seed":30},` +
		`"attach":[{"name":"score.abc","sha256":"` + sha + `","source":"plan:p1/score.abc"}]}`
	for _, c := range []struct {
		name, doc string
		ok        bool
	}{
		{"empty", ``, true},
		{"null", `null`, true},
		{"empty object", `{}`, true},
		{"everything", good, true},
		{"unknown field", `{"x":1}`, false},
		{"unknown nested field", `{"ears":{"lang":"it","zzz":1}}`, false},
		{"trailing data", `{} {}`, false},
		{"not an object", `[]`, false},
		{"wrong type", `{"ears":{"budget_s":"a"}}`, false},
		{"pin without track", `{"inputs":{"src_audio":{"master_sha256":"` + sha + `","bytes":1}}}`, false},
		{"pin with short sha", `{"inputs":{"src_audio":{"track_id":"t","master_sha256":"ab","bytes":1}}}`, false},
		{"pin with upper sha", `{"inputs":{"src_audio":{"track_id":"t","master_sha256":"` + strings.ToUpper(sha) + `","bytes":1}}}`, false},
		{"pin with no bytes", `{"inputs":{"src_audio":{"track_id":"t","master_sha256":"` + sha + `","bytes":0}}}`, false},
		{"inputs without src", `{"inputs":{}}`, false},
		{"ears bad stage", `{"ears":{"stages":["dance"]}}`, false},
		{"ears negative budget", `{"ears":{"budget_s":-1}}`, false},
		{"plan without seeds", `{"plan":{"seeds":[],"seconds_per_seed":30}}`, false},
		{"plan 17 seeds", `{"plan":{"seeds":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17],"seconds_per_seed":30}}`, false},
		{"plan duplicate seed", `{"plan":{"seeds":[1,1],"seconds_per_seed":30}}`, false},
		{"plan no seconds", `{"plan":{"seeds":[1],"seconds_per_seed":0}}`, false},
		{"attach without source", `{"attach":[{"name":"a","sha256":"` + sha + `"}]}`, false},
		{"attach duplicate name", `{"attach":[{"name":"a","sha256":"` + sha + `","source":"s"},{"name":"a","sha256":"` + sha + `","source":"s"}]}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := Parse([]byte(c.doc))
			if (err == nil) != c.ok {
				t.Fatalf("Parse(%q) err = %v, want ok=%v", c.doc, err, c.ok)
			}
			if err != nil {
				return
			}
			b, err := s.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			again, err := Parse(b)
			if err != nil {
				t.Fatalf("canonical form does not parse: %v (%s)", err, b)
			}
			if diff := cmp.Diff(s, again); diff != "" {
				t.Errorf("round trip: %s", diff)
			}
			if b2, _ := again.Canonical(); string(b2) != string(b) {
				t.Errorf("canonical form is not stable: %s vs %s", b, b2)
			}
		})
	}
	if b, _ := (Spec{}).Canonical(); string(b) != `{}` {
		t.Errorf("zero spec canonical = %s, want {}", b)
	}
}

// TestJobspec_NeedsCanonical: needs are sorted, deduplicated, empty for a plain render (so it stays on /1) and always contain
// v2 when non-empty (C322.1).
func TestJobspec_NeedsCanonical(t *testing.T) {
	t.Parallel()
	pin := &Inputs{SrcAudio: &Pin{TrackID: "t", MasterSHA256: sha, Bytes: 1}}
	for _, c := range []struct {
		name string
		kind string
		spec Spec
		want []string
	}{
		{"plain generate", KindGenerate, Spec{}, []string{}},
		{"generate with pins only", KindGenerate, Spec{Inputs: pin}, []string{}},
		{"generate with ears", KindGenerate, Spec{Ears: &Ears{Lang: "it"}}, []string{"ears", "v2"}},
		{"generate with attach", KindGenerate, Spec{Attach: []AttachRef{{Name: "a", SHA256: sha, Source: "s"}}}, []string{"attach", "v2"}},
		{"generate with both", KindGenerate, Spec{Ears: &Ears{}, Attach: []AttachRef{{Name: "a"}}}, []string{"attach", "ears", "v2"}},
		{"analyze", KindAnalyze, Spec{Inputs: pin, Ears: &Ears{}}, []string{"analyze", "ears", "v2"}},
		{"analyze without ears block", KindAnalyze, Spec{Inputs: pin}, []string{"analyze", "v2"}},
		{"plan", KindPlan, Spec{Plan: &Plan{Seeds: []int64{1}, SecondsPerSeed: 30}}, []string{"plan", "v2"}},
		{"plan with attach", KindPlan, Spec{Attach: []AttachRef{{Name: "a"}}}, []string{"attach", "plan", "v2"}},
		{"unknown kind adds nothing", "other", Spec{}, []string{}},
		{"empty kind is generate", "", Spec{Ears: &Ears{}}, []string{"ears", "v2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Needs(c.kind, c.spec)
			if got == nil {
				t.Fatal("Needs returned nil; want a non-nil slice so it marshals to []")
			}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("Needs: %s", diff)
			}
			if !slices.IsSorted(got) {
				t.Errorf("not sorted: %v", got)
			}
		})
	}
}

// TestJobspec_WithCaps: capabilities the routed backend requires are merged into frozen needs, sorted, deduplicated, v2 added.
func TestJobspec_WithCaps(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		needs []string
		caps  []string
		want  []string
	}{
		{"nothing", nil, nil, []string{}},
		{"empty needs and no caps stay on /1", []string{}, nil, []string{}},
		{"adds with v2", []string{}, []string{"agent"}, []string{"agent", "v2"}},
		{"merges and dedups", []string{"ears", "v2"}, []string{"fallback", "ears", "agent"}, []string{"agent", "ears", "fallback", "v2"}},
		{"ignores blanks", []string{"plan", "v2"}, []string{""}, []string{"plan", "v2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := slices.Clone(c.needs)
			got := WithCaps(c.needs, c.caps...)
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("WithCaps: %s", diff)
			}
			if diff := cmp.Diff(in, c.needs); diff != "" {
				t.Errorf("WithCaps mutated its input: %s", diff)
			}
		})
	}
}

// TestJobspec_ParseNeeds: the stored needs_json is a sorted array of known capabilities.
func TestJobspec_ParseNeeds(t *testing.T) {
	t.Parallel()
	if got, err := ParseNeeds(`[]`); err != nil || len(got) != 0 {
		t.Errorf("[] = %v %v", got, err)
	}
	if got, err := ParseNeeds(`["ears","v2"]`); err != nil || !slices.Equal(got, []string{"ears", "v2"}) {
		t.Errorf("= %v %v", got, err)
	}
	for _, bad := range []string{``, `{}`, `["v2"`, `[1]`, `[1,2]`, `null`, `[] x`, `[]{}`} {
		if _, err := ParseNeeds(bad); err == nil {
			t.Errorf("ParseNeeds(%q) accepted", bad)
		}
	}
}
