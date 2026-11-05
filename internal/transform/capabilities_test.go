package transform

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
)

func capsOf(t *testing.T, r *rig, p ...any) Capabilities {
	t.Helper()
	c, err := r.svc.Capabilities(t.Context(), human)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func kindCaps(c Capabilities, k Kind) (KindCapabilities, bool) {
	for _, kc := range c.Kinds {
		if kc.Kind == k {
			return kc, true
		}
	}
	return KindCapabilities{}, false
}

// exampleMember returns the member object of a kind's example with its placeholder ids replaced.
func exampleMember(t *testing.T, kc KindCapabilities) map[string]any {
	t.Helper()
	ex := strings.NewReplacer("<take id>", "id-one", "<other take id>", "id-two").Replace(kc.Example)
	var body map[string]any
	if err := json.Unmarshal([]byte(ex), &body); err != nil {
		t.Fatalf("%s example is not JSON: %v (%s)", kc.Kind, err, kc.Example)
	}
	if body["kind"] != string(kc.Kind) {
		t.Fatalf("%s example kind %v", kc.Kind, body["kind"])
	}
	m, ok := body[string(kc.Kind)].(map[string]any)
	if !ok {
		t.Fatalf("%s example has no member", kc.Kind)
	}
	return m
}

// TestCapabilities_MatchesValidation: every listed range is enforced (min and max accepted, just outside refused with range), every
// listed enum value is accepted and a stranger refused, every listed name is a known parameter and an unlisted one is not, and
// every example passes validation.
func TestCapabilities_MatchesValidation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := capsOf(t, r)
	if !c.Enabled || c.DSPVersion != DSPVersion {
		t.Fatalf("caps %+v", c)
	}
	// the nine kinds of the plan, minus score_render (T1b, listed only when enabled), are the registry
	got := []Kind{}
	for _, kc := range c.Kinds {
		got = append(got, kc.Kind)
	}
	want := []Kind{}
	for _, k := range RegisteredKinds() {
		if k != KindScoreRender {
			want = append(want, k)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("kinds %v, want %v", got, want)
	}
	for _, kc := range c.Kinds {
		ks, _ := LookupKind(kc.Kind)
		if kc.Summary == "stand-in" { // main_test.go's placeholder for a kind whose lane has not merged: real entries are held to this test
			continue
		}
		if kc.Summary == "" || kc.Inputs < 1 || len(kc.Params) == 0 || kc.Example == "" {
			t.Errorf("%s entry is incomplete: %+v", kc.Kind, kc)
		}
		base := exampleMember(t, kc)
		validate := func(m map[string]any) error {
			raw, _ := json.Marshal(m)
			_, err := ks.Validate(raw)
			return err
		}
		if err := validate(base); err != nil {
			t.Errorf("%s example refused: %v", kc.Kind, err)
		}
		if err := validate(map[string]any{"zzz_unlisted": 1}); err == nil {
			t.Errorf("%s accepted an unlisted parameter", kc.Kind)
		}
		for _, p := range kc.Params {
			with := func(v any) map[string]any {
				m := map[string]any{}
				for k, x := range base {
					m[k] = x
				}
				// parameters that exclude or bound each other: take the example's companions out of the way
				switch {
				case kc.Kind == KindShepard && p.Name == "loop_bars":
					delete(m, "loop_s")
				case kc.Kind == KindShepard && p.Name == "duration_s":
					m["period_s"] = 8
				}
				m[p.Name] = v
				return m
			}
			isRange := (p.Type == "number" || p.Type == "integer") && (p.Min != nil || p.Max != nil)
			if isRange {
				step := 1.0
				if p.Type == "number" {
					step = 0.5
				}
				if p.Min != nil {
					if err := validate(with(*p.Min)); err != nil {
						t.Errorf("%s.%s min %v refused: %v", kc.Kind, p.Name, *p.Min, err)
					}
					wantRange(t, validate(with(*p.Min-step)), kc.Kind, p.Name)
				}
				if p.Max != nil {
					if err := validate(with(*p.Max)); err != nil {
						t.Errorf("%s.%s max %v refused: %v", kc.Kind, p.Name, *p.Max, err)
					}
					wantRange(t, validate(with(*p.Max+step)), kc.Kind, p.Name)
				}
			}
			for _, e := range p.Enum {
				if err := validate(with(e)); err != nil {
					t.Errorf("%s.%s enum %q refused: %v", kc.Kind, p.Name, e, err)
				}
			}
			if len(p.Enum) > 0 {
				wantRange(t, validate(with("not-a-member")), kc.Kind, p.Name)
			}
			if p.Required && p.Type == "track" {
				m := with("x")
				delete(m, p.Name)
				var re *RuleError
				if err := validate(m); !errors.As(err, &re) || re.Rule != RuleRequired || re.Field != p.Name {
					t.Errorf("%s.%s required not enforced: %v", kc.Kind, p.Name, err)
				}
			}
			// a listed name is never an unknown field
			var ue *RuleError
			if err := validate(with(nil)); errors.As(err, &ue) && ue.Rule == RuleUnknownField {
				t.Errorf("%s lists %s but refuses it as unknown", kc.Kind, p.Name)
			}
		}
	}
}

func wantRange(t *testing.T, err error, k Kind, name string) {
	t.Helper()
	var e *RuleError
	if !errors.As(err, &e) || e.Field != name || e.Rule != RuleRange {
		t.Errorf("%s.%s outside the listed range: got %v, want range", k, name, err)
	}
}

// TestCapabilities_ShepardMuddyNote: the required sentences of C205.4, C212.5 and C223.2.
func TestCapabilities_ShepardMuddyNote(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := capsOf(t, r)
	sh, _ := kindCaps(c, KindShepard)
	if !slices.Contains(sh.BlindSpots, "a shepard of five layers of a full mix is expected to sound muddy; use a pad-heavy, drum-free loop") {
		t.Errorf("shepard blind spots %v", sh.BlindSpots)
	}
	en, _ := kindCaps(c, KindEnsemble)
	if !strings.Contains(strings.Join(en.BlindSpots, " "), "full mix") {
		t.Errorf("ensemble blind spots %v", en.BlindSpots)
	}
	for _, k := range []Kind{KindMorph, KindPolytempo} {
		kc, _ := kindCaps(c, k)
		j := strings.Join(kc.BlindSpots, " ")
		for _, w := range []string{"rubato", "four-on-the-floor", "one-drop", "half-time"} {
			if !strings.Contains(j, w) {
				t.Errorf("%s blind spots lack %q", k, w)
			}
		}
	}
	if !strings.Contains(c.QuotaNote, "shared pool per principal kind") {
		t.Errorf("quota note %q", c.QuotaNote)
	}
}

// TestCapabilities_Content: limits from the configuration, the caller's own pool, queue, tools, soundfonts without paths, and the
// hi-fi fields passed through from whatever is registered.
func TestCapabilities_Content(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sf := filepath.Join(dir, "secret-name.sf2")
	if err := os.WriteFile(sf, make([]byte, 1234), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRig(t, func(c *config.Config, o *Options) {
		c.TransformMaxOutS, c.TransformMaxMemMB, c.TransformMaxCPUS, c.TransformQueueMax = 300, 512, 120, 3
		c.Soundfonts = []config.Soundfont{{Name: "orchestra", Path: sf}}
		o.ToolStatus = func() ToolStatus {
			return ToolStatus{FFmpeg: true, FLAC: true, Fluidsynth: true, FluidsynthVersion: "2.3.4"}
		}
		o.SoundfontSHA12 = func(config.Soundfont) string { return "abcdef012345" }
	})
	r.create(agentA, req(KindEnsemble, fmt.Sprintf(`{"source_id":%q}`, r.tx("A"))))
	c, err := r.svc.Capabilities(t.Context(), agentA)
	if err != nil {
		t.Fatal(err)
	}
	if c.Limits != (LimitsInfo{MaxInputs: 2, MaxInputS: 600, MaxOutputS: 300, MaxMemMB: 512, MaxCPUS: 120, Concurrent: 1}) {
		t.Errorf("limits %+v", c.Limits)
	}
	if c.Queue != (QueueInfo{Queued: 1, Max: 3}) || c.Quota.Kind != "agent" || c.Quota.CapMS != 1_800_000 || c.Quota.ReservedMS == 0 {
		t.Errorf("queue %+v quota %+v", c.Queue, c.Quota)
	}
	if !c.Tools.Fluidsynth || c.Tools.FluidsynthVersion != "2.3.4" || len(c.Soundfonts) != 1 || c.Soundfonts[0] != (SoundfontInfo{Name: "orchestra", SHA12: "abcdef012345", Bytes: 1234}) {
		t.Errorf("tools %+v soundfonts %+v", c.Tools, c.Soundfonts)
	}
	if strings.Contains(fmt.Sprintf("%+v", c), "secret-name") || strings.Contains(fmt.Sprintf("%+v", c), dir) {
		t.Error("capabilities leak a soundfont path")
	}
	// score_render is listed only when T1b is enabled: the kind is not registered here, so it is absent even with a soundfont
	if _, ok := kindCaps(c, KindScoreRender); ok {
		t.Error("score_render listed")
	}
	// the hi-fi fields pass through for whichever binaural entry is registered
	if bn, ok := kindCaps(c, KindBinaural); ok && bn.Listening != "headphones" {
		t.Errorf("binaural listening %q", bn.Listening)
	}
	if gl, _ := kindCaps(c, KindGlacier); gl.Listening != "" || gl.Dataset != "" {
		t.Errorf("glacier carries hi-fi fields: %+v", gl)
	}
	// disabled: 503
	r.svc.o.Config.TransformEnabled = false
	_, err = r.svc.Capabilities(t.Context(), human)
	wantProblem(t, err, 503, "UNAVAILABLE")
}
