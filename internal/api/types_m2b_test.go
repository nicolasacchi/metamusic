package api_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/tokens"
	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
)

var updateFixtures = flag.Bool("update", false, "rewrite web/src/lib/fixtures/m2b/*.json")

func openapiDoc(t *testing.T) map[string]any {
	t.Helper()
	a, _ := newTestAPI(t)
	b, err := api.OpenAPIJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func schemas(doc map[string]any) map[string]any {
	return doc["components"].(map[string]any)["schemas"].(map[string]any)
}

// typeSet lists the JSON types of a schema ("object", "null", ...).
func typeSet(s map[string]any) (types []string) {
	switch v := s["type"].(type) {
	case string:
		types = append(types, v)
	case []any:
		for _, x := range v {
			types = append(types, x.(string))
		}
	}
	return types
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// m2bComponents are the response components of the M2b operations.
var m2bComponents = []string{"Duel", "Slot", "DuelSlots", "Verdict", "DuelSummary", "DuelSlotsSummary", "SlotSummary", "DuelPage", "NextDuel", "SubmitResult",
	"LadderPage", "LadderItem", "Dossier", "SpectrogramView", "Comparison", "CompareLadder", "ClipLink", "TrackStructure", "SpectrogramInfo",
	"ComparisonSide", "ComparisonDeltas", "RecipeDiff"}

// nullableObjects are the `|null` objects of C51/C53; every other object-valued field is not nullable.
var nullableObjects = map[string]bool{
	"Slot.track": true, "Duel.my_verdict": true, "Duel.human_verdict": true,
	"SlotSummary.track": true, "DuelSummary.human_verdict": true, "NextDuel.duel": true,
	"CompareLadder.a": true, "CompareLadder.b": true,
	"Dossier.loudness": true, "Dossier.spectrum": true, "Dossier.silence": true, "Dossier.clipping": true, "Dossier.token_loops": true,
}

func TestOpenAPI_M2bNullableObjects(t *testing.T) {
	t.Parallel()
	all := schemas(openapiDoc(t))
	seen := map[string]bool{}
	for _, name := range m2bComponents {
		comp, ok := all[name].(map[string]any)
		if !ok {
			t.Errorf("component %s missing", name)
			continue
		}
		props, _ := comp["properties"].(map[string]any)
		for pname, raw := range props {
			ps := raw.(map[string]any)
			var isObject bool
			if ref, ok := ps["$ref"].(string); ok {
				target := all[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
				isObject = has(typeSet(target), "object")
			} else {
				isObject = has(typeSet(ps), "object")
			}
			nullable := has(typeSet(ps), "null")
			key := name + "." + pname
			switch {
			case nullableObjects[key]:
				seen[key] = true
				if !isObject || !nullable {
					t.Errorf("%s must be a nullable inline object, got %v", key, ps)
				}
			case isObject && nullable:
				t.Errorf("%s is nullable but not listed in C51/C53", key)
			}
		}
	}
	for k := range nullableObjects {
		if !seen[k] {
			t.Errorf("%s not found in the spec", k)
		}
	}
}

// Every exported field reachable from an M2b response carries an explicit json tag, so Go field names never reach REST or MCP.
func TestJSONTags_M2bPayloads(t *testing.T) {
	t.Parallel()
	roots := []reflect.Type{
		reflect.TypeFor[duels.Duel](), reflect.TypeFor[duels.NextResult](), reflect.TypeFor[duels.Page](), reflect.TypeFor[duels.SubmitResult](),
		reflect.TypeFor[duels.LadderPage](), reflect.TypeFor[ears.Dossier](), reflect.TypeFor[ears.SpectrogramView](), reflect.TypeFor[ears.Comparison](),
		reflect.TypeFor[ears.Perception](), reflect.TypeFor[api.ClipLink](), reflect.TypeFor[analysis.Silence](), reflect.TypeFor[analysis.Clipping](),
		reflect.TypeFor[tokens.Report](), reflect.TypeFor[ladder.PairStats](), reflect.TypeFor[ladder.Strength](),
	}
	seen := map[reflect.Type]bool{}
	var walk func(path string, ty reflect.Type)
	walk = func(path string, ty reflect.Type) {
		for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Array || ty.Kind() == reflect.Map {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct || seen[ty] || ty.PkgPath() == "time" {
			return
		}
		seen[ty] = true
		for i := range ty.NumField() {
			f := ty.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Anonymous && f.Tag.Get("json") == "" {
				walk(path+"."+f.Name, f.Type) // flattened embedded struct
				continue
			}
			if name == "" || name == "-" {
				t.Errorf("%s.%s (%s) has no explicit json tag", path, f.Name, ty)
			}
			walk(path+"."+f.Name, f.Type)
		}
	}
	for _, r := range roots {
		walk(r.Name(), r)
	}
	if len(seen) < 25 {
		t.Errorf("walked only %d struct types", len(seen))
	}
}

// ---- web fixtures ----

const (
	sha1hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sha2hex = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func ptr[T any](v T) *T { return &v }

func clipRef(sha string, secs float64) duels.ClipRef {
	return duels.ClipRef{URL: "/media/" + sha + ".flac?exp=1791000000&sig=c3ludGhldGljLXNpZw", ExpiresAt: "2026-10-14T00:00:00Z", TTLS: 43200,
		ContentType: "audio/flac", Bytes: 5_400_000, DurationS: secs}
}

func fixtureDuels() (open, revealed duels.Duel) {
	open = duels.Duel{ID: "01900000-0000-7000-8000-000000000b01", State: "open", Blind: true, Revealed: false,
		CreatedAt: "2026-10-13T11:00:00Z", ExpiresAt: "2026-11-12T11:00:00Z", CreatedByKind: "human",
		Window: duels.Window{FromS: 16.3, ToS: 61.3, DurationS: 45}, LoudnessMatched: true,
		Slots:          duels.Slots{A: duels.Slot{Clip: clipRef(sha1hex, 45)}, B: duels.Slot{Clip: clipRef(sha2hex, 45)}},
		CriticVerdicts: []duels.Verdict{}}
	hv := duels.Verdict{ID: "01900000-0000-7000-8000-000000000c01", Source: "human", PrincipalKind: "human", Verdict: "a",
		WinnerTrackID: ptr("01900000-0000-7000-8000-000000000a02"), Note: ptr("synthetic note"), LatencyMS: ptr(int64(8200)), Switches: ptr(5),
		SawReveal: false, CreatedAt: "2026-10-13T11:05:00Z"}
	cv := duels.Verdict{ID: "01900000-0000-7000-8000-000000000c02", Source: "critic", PrincipalKind: "agent", Verdict: "b",
		WinnerTrackID: ptr("01900000-0000-7000-8000-000000000a01"), SawReveal: true, CreatedAt: "2026-10-13T11:06:00Z"}
	trk := func(id, title string, seed int64, req string, dur float64) *duels.TrackBriefOrNull {
		return &duels.TrackBriefOrNull{ID: id, Title: title, Seed: seed, RequestID: req, RootTrackID: "01900000-0000-7000-8000-000000000a01", DurationS: dur}
	}
	revealed = open
	revealed.ID, revealed.State, revealed.Revealed = "01900000-0000-7000-8000-000000000b02", "decided", true
	revealed.Slots = duels.Slots{
		A: duels.Slot{Clip: clipRef(sha1hex, 45), Track: trk("01900000-0000-7000-8000-000000000a02", "Synthetic Beta", 2002, "syn-beta", 66.6), GainDB: ptr(0.0), WindowLUFS: ptr(-20.0)},
		B: duels.Slot{Clip: clipRef(sha2hex, 45), Track: trk("01900000-0000-7000-8000-000000000a01", "Synthetic Alpha", 2001, "syn-alpha", 61.3), GainDB: ptr(-7.37), WindowLUFS: ptr(-12.63)},
	}
	mv, hvo := duels.VerdictOrNull(hv), duels.VerdictOrNull(hv)
	revealed.MyVerdict, revealed.HumanVerdict = &mv, &hvo
	revealed.CriticVerdicts = []duels.Verdict{cv}
	return open, revealed
}

func fixtureLadder() duels.LadderPage {
	item := func(rank int, id, title string, seed int64, u, sd float64, n, w, l, ti, bb int, prov bool) duels.LadderItem {
		return duels.LadderItem{Rank: rank, TrackID: id, Title: title, RootTrackID: "01900000-0000-7000-8000-000000000a01", Seed: seed,
			U: u, SD: sd, CI95: [2]float64{u - 1.96*sd, u + 1.96*sd}, NDuels: n, Wins: w, Losses: l, Ties: ti, BothBad: bb, Provisional: prov}
	}
	return duels.LadderPage{Scope: "lineage", RootTrackID: ptr("01900000-0000-7000-8000-000000000a01"), IncludeSighted: false, GeneratedAt: "2026-10-13T12:00:00Z",
		Model:       duels.LadderModel{Kind: "btd", PriorSD: 1.5, NuPrior: "gamma(2,2)", Anchor: "reject", AnchorU: 0, Nu: 0.62, Iterations: 41, Converged: true},
		Counts:      duels.LadderCounts{VerdictsHumanCounted: 9, VerdictsCriticExcluded: 3, VerdictsSightedExcluded: 1, BothBad: 1, Ties: 2, Tracks: 3},
		Diagnostics: duels.LadderDiagnostics{SlotAWinRate: ptr(0.5), NAB: 6},
		Items: []duels.LadderItem{
			item(1, "01900000-0000-7000-8000-000000000a02", "Synthetic Beta", 2002, 1.2312, 0.61, 6, 4, 1, 1, 0, false),
			item(2, "01900000-0000-7000-8000-000000000a01", "Synthetic Alpha", 2001, 0.4107, 0.58, 6, 2, 3, 1, 0, false),
			item(3, "01900000-0000-7000-8000-000000000a03", "Synthetic Gamma", 2003, -0.9, 1.1, 2, 0, 1, 0, 1, true),
		}, Total: 3}
}

func fixtureDossier() ears.Dossier {
	gmin, gmed := 0.12, 0.35
	return ears.Dossier{TrackID: "01900000-0000-7000-8000-000000000101", Tier: "A", Complete: true, Missing: []string{},
		Summary: "-14.0 LUFS, true peak -1.2 dBTP, bandwidth 16.0 kHz; 2 warnings: clipping 2.0 s at 0:02; token loop suspected at 0:03",
		Loudness: &library.LoudnessOut{Loudness: analysis.Loudness{IntegratedLUFS: -14, LRA: 5.5, LRALowLUFS: -19, LRAHighLUFS: -13, TruePeakDBTP: -1.2, SamplePeakDBFS: -1.5,
			ShortTermMaxLUFS: -12, ShortTerm1Hz: []float64{-14, -13.5}}, ReplayGainTrackGainDB: -4, Version: "mm-loudness/1 ffmpeg n9.0.1"},
		Spectrum: &library.SpectrumOut{Spectrum: analysis.Spectrum{BandwidthHz: ptr(16000), RefDB: -40, ThresholdDB: -110, Frames: 30}, Version: "mm-spectrum/1 ffmpeg n9.0.1"},
		Silence:  &ears.SilenceBlockOrNull{Silence: analysis.Silence{ThresholdDB: -50, MinDurationS: 1, Spans: []analysis.Span{{StartS: 4, EndS: 6.5}}, InternalS: 2.5, TotalS: 2.5, DurationS: 12}, Version: "mm-silence/1 ffmpeg n9.0.1"},
		Clipping: &ears.ClippingBlockOrNull{Clipping: analysis.Clipping{FrameS: 0.1, PeakDBFS: 0, FullScaleFrames: 20, ClippedFrames: 20, ClippedS: 2, Spans: []analysis.Span{{StartS: 2, EndS: 4}}}, Version: "mm-clipping/1 ffmpeg n9.0.1"},
		TokenLoops: &ears.TokenLoopsBlockOrNull{ArtifactSHA256: sha1hex, DType: "<i8", Shape: []int{1, 600}, TimeAxis: 1, Version: "mm-token-loops/1 go1.27",
			Streams: []tokens.Stream{{Index: 0, NTokens: 600, DistinctTokens: 140, TokensPerS: 50, Distinct4GramRatio: 0.41,
				LongestRepeat: tokens.Repeat{Tokens: 300, S: 6, FirstAtS: 3, SecondAtS: 9},
				PeriodicRun:   tokens.Periodic{PeriodTokens: 25, PeriodS: 0.5, SpanTokens: 325, SpanS: 6.5, AtS: 3, Repeats: 13},
				GzipRatio10s:  []float64{0.35}, GzipRatioMin: &gmin, GzipRatioMedian: &gmed, Encoding: "u16le"}}},
		Structure: ears.Structure{DurationS: 12, Timed: true, Source: ptr("plan"), Confidence: ptr(0.7), ExpectedDurationS: ptr(12.0), Ratio: ptr(1.0),
			Sections: []ears.StructureSection{{Idx: 0, Label: "verse 1", Kind: "verse", StartS: ptr(0.0), EndS: ptr(4.0)}, {Idx: 1, Label: "chorus 1", Kind: "chorus", StartS: ptr(4.0), EndS: ptr(8.0)}}},
		Flags: []ears.Flag{
			{Code: "clipping", Severity: "warn", Evidence: map[string]any{"clipped_s": 2.0}},
			{Code: "token_loop_suspected", Severity: "warn", Evidence: map[string]any{"stream": 0, "at_s": 3.0, "span_s": 6.5, "period_s": 0.5}}},
		BlindSpots:  analysis.BlindSpots(analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: "plan", Confidence: ptr(0.7)}),
		Spectrogram: ears.SpectrogramInfo{Available: true, Bytes: ptr(int64(61000)), Width: ptr(1308), Height: ptr(560), Quality: ptr(85)},
		Provenance:  ears.Provenance{Analyzers: map[string]string{"silence": "mm-silence/1 ffmpeg n9.0.1", "clipping": "mm-clipping/1 ffmpeg n9.0.1"}}}
}

func fixtureSpectrogram() ears.SpectrogramView {
	return ears.SpectrogramView{TrackID: "01900000-0000-7000-8000-000000000101", ContentType: "image/jpeg", SHA256: sha2hex, Bytes: 61000, Width: 1308, Height: 560, Quality: 85,
		Plot:    ears.SpectrogramPlot{X0: 142, Y0: 112, Width: 1024, Height: 384, DurationS: 12, FreqScale: "log"},
		Overlay: ears.SpectrogramOverlay{Timed: true, Source: ptr("plan"), Confidence: ptr(0.7), LineTiming: "none"},
		URL:     "/media/" + sha2hex + ".jpg?exp=1791000000&sig=c3ludGhldGljLXNpZw", ExpiresAt: "2026-10-14T00:00:00Z", TTLS: 43200,
		BlindSpots: analysis.BlindSpots(analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: "plan", Confidence: ptr(0.7), Spectrogram: true})}
}

func fixtureComparison() ears.Comparison {
	return ears.Comparison{
		A:          ears.Side{TrackID: "01900000-0000-7000-8000-000000000a01", Title: "Synthetic Alpha", Seed: 2001, RequestID: "syn-alpha", RootTrackID: "01900000-0000-7000-8000-000000000a01", DurationS: 61.3, Tier: "A", Flags: []string{"clipping"}},
		B:          ears.Side{TrackID: "01900000-0000-7000-8000-000000000a02", Title: "Synthetic Beta", Seed: 2002, RequestID: "syn-alpha", RootTrackID: "01900000-0000-7000-8000-000000000a01", DurationS: 66.6, Tier: "0", Flags: []string{}},
		Relation:   "siblings",
		Deltas:     ears.Deltas{IntegratedLUFS: ptr(-2.1), TruePeakDBTP: ptr(-0.4), LRALU: ptr(0.3), BandwidthHz: ptr(-500.0), DurationS: ptr(5.3), ClippedS: ptr(-2.0), InternalSilenceS: ptr(0.0)},
		Flags:      ears.FlagDiff{OnlyA: []string{"clipping"}, OnlyB: []string{}, Both: []string{}},
		RecipeDiff: ears.RecipeDiff{SameHash: false, Changed: []string{"seed"}, Seed: ears.SeedPair{A: 2001, B: 2002}, StyleTerms: ears.StyleTermDiff{Added: []string{}, Removed: []string{}}},
		Duels:      ladder.PairStats{Human: ladder.Counts{AWins: 1, BWins: 3, Ties: 1, N: 5}, HumanBlindN: 4, Critic: ladder.Counts{BWins: 1, N: 1}},
		Ladder:     ears.CompareLadder{A: &ears.StrengthOrNull{U: 0.41, SD: 0.58, NDuels: 6}, B: &ears.StrengthOrNull{U: 1.23, SD: 0.61, NDuels: 6}, Nu: 0.62, PABeatsB: ptr(0.22), PTie: ptr(0.31)},
		BlindSpots: analysis.BlindSpots(analysis.BlindSpotFacts{TierA: false, Timed: true, SectionSource: "plan", Confidence: ptr(0.7)}),
	}
}

// fixtureFiles maps a fixture file to its schema component and value.
func fixtureFiles() map[string]struct {
	schema string
	value  any
} {
	open, revealed := fixtureDuels()
	return map[string]struct {
		schema string
		value  any
	}{
		"duel_open_blind.json":  {"Duel", open},
		"duel_revealed.json":    {"Duel", revealed},
		"ladder_page.json":      {"LadderPage", fixtureLadder()},
		"dossier.json":          {"Dossier", fixtureDossier()},
		"spectrogram_view.json": {"SpectrogramView", fixtureSpectrogram()},
		"comparison.json":       {"Comparison", fixtureComparison()},
	}
}

func fixtureDir() string { return filepath.Join("..", "..", "web", "src", "lib", "fixtures", "m2b") }

// resolvedSchema returns component name as a schema whose refs point into $defs (the whole components map).
func resolvedSchema(t *testing.T, doc map[string]any, name string) *jsonschema.Resolved {
	t.Helper()
	defs, _ := json.Marshal(schemas(doc))
	root := `{"$ref":"#/$defs/` + name + `","$defs":` + strings.ReplaceAll(string(defs), "#/components/schemas/", "#/$defs/") + `}`
	var s jsonschema.Schema
	if err := json.Unmarshal([]byte(root), &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return rs
}

func marshalFixture(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		t.Fatal(err)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// TestWebFixtures_M2b serialises typed Go values into web/src/lib/fixtures/m2b/*.json (-update), checks the
// committed files are current and validates every value against its response schema in the OpenAPI document.
func TestWebFixtures_M2b(t *testing.T) {
	doc := openapiDoc(t)
	for name, fx := range fixtureFiles() {
		raw := marshalFixture(t, fx.value)
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		if err := resolvedSchema(t, doc, fx.schema).Validate(v); err != nil {
			t.Errorf("%s violates %s: %v", name, fx.schema, err)
		}
		path := filepath.Join(fixtureDir(), name)
		if *updateFixtures {
			if err := os.MkdirAll(fixtureDir(), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v (run go test ./internal/api -run TestWebFixtures_M2b -update)", name, err)
			continue
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("%s is stale (run go test ./internal/api -run TestWebFixtures_M2b -update)", name)
		}
	}
}
