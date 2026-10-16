package trackctx_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
	"github.com/nicolasacchi/metamusic/internal/trackctx/trackctxtest"
)

// newEarsEnv is newEnv plus the earstest rows: Luna and Citta carry Tier A and a spectrogram rendition.
func newEarsEnv(t *testing.T) (*env, earstest.Ears) {
	t.Helper()
	e := newEnv(t, true)
	data, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	fx := earstest.Fixture(t, e.st, data, lt.Fixtures{}, lt.FixedNow)
	return e, fx
}

type perceptionOut struct {
	Tier        string          `json:"tier"`
	LUFSI       *float64        `json:"lufs_i"`
	TruePeakDB  *float64        `json:"true_peak_db"`
	LRALU       *float64        `json:"lra_lu"`
	BandwidthHz *float64        `json:"bandwidth_hz"`
	Sections    json.RawMessage `json:"sections"`
	Timed       bool            `json:"timed"`
	Flags       []string        `json:"flags"`
	BlindSpots  []string        `json:"blind_spots"`
}

func perceptionOf(t *testing.T, e *env, id string) (perceptionOut, []byte) {
	t.Helper()
	b, err := e.b.Build(context.Background(), agent, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	var got perceptionOut
	if err := json.Unmarshal(section(t, raw, "perception"), &got); err != nil {
		t.Fatal(err)
	}
	return got, section(t, raw, "perception")
}

func TestContext_PerceptionFromEars(t *testing.T) {
	t.Parallel()
	e, fx := newEarsEnv(t)
	wantKeys := []string{"tier", "lufs_i", "true_peak_db", "lra_lu", "bandwidth_hz", "sections", "timed", "flags", "blind_spots", "tempo_bpm", "tempo_confidence"}

	got, raw := perceptionOf(t, e, fx.ClipTrack)
	if got.Tier != "A" || !slices.Contains(got.Flags, "clipping") || !slices.Contains(got.Flags, "internal_silence") {
		t.Errorf("Tier A track: tier %q flags %v", got.Tier, got.Flags)
	}
	if got.LRALU == nil || got.LUFSI == nil || got.TruePeakDB == nil || got.BandwidthHz == nil {
		t.Errorf("loudness values missing: %+v", got)
	}
	if diff := cmp.Diff(wantKeys, keys(t, raw)); diff != "" {
		t.Errorf("keys (-want +got):\n%s", diff)
	}
	if loop, _ := perceptionOf(t, e, fx.LoopTrack); !loop.Timed || !slices.Contains(loop.Flags, "token_loop_suspected") {
		t.Errorf("loop track: timed %v flags %v", loop.Timed, loop.Flags)
	}

	// a track without Tier A
	plain := newEnv(t, true)
	none, raw := perceptionOf(t, plain, lt.TrackLuna)
	if none.Tier != "0" || none.Flags == nil || len(none.Flags) != 0 {
		t.Errorf("no Tier A: tier %q flags %v (want [] not null)", none.Tier, none.Flags)
	}
	if !slices.ContainsFunc(none.BlindSpots, func(s string) bool { return strings.Contains(s, "Tier A not run yet") }) {
		t.Errorf("blind spots lack the Tier A entry: %v", none.BlindSpots)
	}
	if diff := cmp.Diff(wantKeys, keys(t, raw)); diff != "" {
		t.Errorf("keys without Tier A (-want +got):\n%s", diff)
	}

	// M2a's values are unchanged: Luna's golden loudness and the section shape
	luna, _ := perceptionOf(t, e, lt.TrackLuna)
	if luna.LUFSI == nil || *luna.LUFSI != -14 || luna.TruePeakDB == nil || *luna.TruePeakDB != -1.2 || luna.LRALU == nil || *luna.LRALU != 5.5 ||
		luna.BandwidthHz == nil || *luna.BandwidthHz != 16000 {
		t.Errorf("luna loudness changed: %+v", luna)
	}
	var secs []map[string]any
	if err := json.Unmarshal(luna.Sections, &secs); err != nil || len(secs) == 0 {
		t.Fatalf("sections: %v %s", err, luna.Sections)
	}
	if diff := cmp.Diff([]string{"label", "start", "end", "source", "confidence"}, keys(t, mustFirstSection(t, luna.Sections))); diff != "" {
		t.Errorf("section keys (-want +got):\n%s", diff)
	}
}

func mustFirstSection(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var all []json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil || len(all) == 0 {
		t.Fatalf("sections: %v", err)
	}
	return all[0]
}

func TestContext_SpectrogramLink(t *testing.T) {
	t.Parallel()
	e, fx := newEarsEnv(t)
	links := func(p auth.Principal, id string) map[string]any {
		t.Helper()
		b, err := e.b.Build(context.Background(), p, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		return b.Links
	}
	l := links(agent, fx.LoopTrack)
	whole, err := e.b.Build(context.Background(), agent, fx.LoopTrack, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"stream", "spectrogram", "comments", "track"}, keys(t, section(t, marshal(t, whole), "_links"))); diff != "" {
		t.Errorf("_links order (-want +got):\n%s", diff)
	}
	link, _ := l["spectrogram"].(string)
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.Host != "mm.example.com" || !strings.HasPrefix(u.Path, "/media/"+fx.SpectroSHA+".jpg") {
		t.Fatalf("spectrogram link = %q (%v)", link, err)
	}
	// TTL: 15 minutes for an agent, 12 hours for the operator
	exp := func(p auth.Principal) int64 {
		t.Helper()
		l, _ := links(p, fx.LoopTrack)["spectrogram"].(string)
		u, _ := url.Parse(l)
		var n int64
		_, _ = fmt.Sscan(u.Query().Get("exp"), &n)
		return n
	}
	if got, want := exp(agent), lt.FixedNow.Add(15*time.Minute).Unix(); got != want {
		t.Errorf("agent expiry %d, want %d (%s)", got, want, link)
	}
	if got, want := exp(human), lt.FixedNow.Add(12*time.Hour).Unix(); got != want {
		t.Errorf("human expiry %d, want %d", got, want)
	}
	// no rendition => null
	if v, ok := links(agent, lt.TrackTwinA)["spectrogram"]; !ok || v != nil {
		t.Errorf("without a rendition: %v (present %v), want null", v, ok)
	}
}

// ladderEnv builds a Builder over the duels fixture with the real duels service.
func ladderEnv(t *testing.T, mutate func(*env)) (*env, *duels.Service) {
	t.Helper()
	e := newEnv(t, true)
	duelstest.Fixture(t, e.st, "", lt.Fixtures{}, lt.FixedNow, nil)
	if mutate != nil {
		mutate(e)
	}
	ds := duels.New(duels.Options{Store: e.st, Library: e.lib, Now: e.now})
	e.b.UseM2b(ears.New(ears.Options{Store: e.st, Library: e.lib, Now: e.now}), ds)
	return e, ds
}

func ladderOf(t *testing.T, e *env, id string) map[string]any {
	t.Helper()
	b, err := e.b.Build(context.Background(), agent, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	var got struct {
		Judgments struct {
			Ladder map[string]any `json:"ladder"`
		} `json:"judgments"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got.Judgments.Ladder
}

func TestContext_LadderHumanOnly(t *testing.T) {
	t.Parallel()
	exec := func(e *env, q string, a ...any) {
		t.Helper()
		if _, err := e.st.W.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	// without counted duels the ladder is null
	plain := newEnv(t, true)
	plain.b.UseM2b(ears.New(ears.Options{Store: plain.st, Library: plain.lib, Now: plain.now}),
		duels.New(duels.Options{Store: plain.st, Library: plain.lib, Now: plain.now}))
	if l := ladderOf(t, plain, lt.TrackLuna); l != nil {
		t.Errorf("track without duels: ladder %v, want null", l)
	}

	base, _ := ladderEnv(t, nil)
	human := ladderOf(t, base, duelstest.TrackAlpha)
	if human == nil || human["n_duels"] == nil {
		t.Fatalf("Alpha has counted human verdicts but ladder = %v", human)
	}
	if diff := cmp.Diff([]string{"u", "sd", "n_duels"}, mapKeys(t, base, duelstest.TrackAlpha)); diff != "" {
		t.Errorf("ladder keys (-want +got):\n%s", diff)
	}

	// the critic's verdict (it crowned Alpha) never counts: it only matters once it is a human one
	const ins = `INSERT INTO duel_verdicts(id, duel_id, source, principal_kind, principal_id, verdict, winner_track_id, latency_ms, switches, saw_reveal, created_at)
		VALUES (?, ?, ?, ?, ?, 'a', ?, 5000, 3, 0, ?)`
	at := lt.FixedNow.Format(time.RFC3339)
	moreCritic, _ := ladderEnv(t, func(e *env) {
		exec(e, ins, "01900000-0000-7000-8000-000000000c91", duelstest.DuelOpen, "critic", "agent", lt.FixtureAgent, duelstest.TrackAlpha, at)
	})
	if diff := cmp.Diff(human, ladderOf(t, moreCritic, duelstest.TrackAlpha)); diff != "" {
		t.Errorf("a critic verdict changed the ladder (-before +after):\n%s", diff)
	}
	// a device verdict counts
	moreDevice, _ := ladderEnv(t, func(e *env) {
		exec(e, ins, "01900000-0000-7000-8000-000000000c92", duelstest.DuelOpen, "human", "device", lt.FixtureDev, duelstest.TrackAlpha, at)
	})
	if cmp.Equal(human, ladderOf(t, moreDevice, duelstest.TrackAlpha)) {
		t.Error("a device verdict did not change the ladder")
	}

	// children[].ladder equals the global u/sd of get_ladder
	_, ds := ladderEnv(t, nil)
	ss, _, err := ds.Strengths(context.Background(), []string{duelstest.TrackBeta})
	if err != nil {
		t.Fatal(err)
	}
	b, err := base.b.Build(context.Background(), agent, duelstest.TrackAlpha, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Lineage struct {
			Children []struct {
				ID     string         `json:"id"`
				Ladder map[string]any `json:"ladder"`
			} `json:"children"`
		} `json:"lineage"`
	}
	if err := json.Unmarshal(marshal(t, b), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Lineage.Children) != 1 || got.Lineage.Children[0].ID != duelstest.TrackBeta {
		t.Fatalf("children = %+v", got.Lineage.Children)
	}
	if diff := cmp.Diff(map[string]any{"u": ss[duelstest.TrackBeta].U, "sd": ss[duelstest.TrackBeta].SD}, got.Lineage.Children[0].Ladder); diff != "" {
		t.Errorf("child ladder != global strength (-want +got):\n%s", diff)
	}
}

func mapKeys(t *testing.T, e *env, id string) []string {
	t.Helper()
	b, err := e.b.Build(context.Background(), agent, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return keys(t, section(t, section(t, marshal(t, b), "judgments"), "ladder"))
}

// fakeStrengths gives every id a ladder.
type fakeStrengths struct{}

func (fakeStrengths) Strengths(_ context.Context, ids []string) (map[string]ladder.Strength, float64, error) {
	out := map[string]ladder.Strength{}
	for i, id := range ids {
		out[id] = ladder.Strength{U: float64(i) + 0.123456, SD: 0.987654, NDuels: i + 1}
	}
	return out, 4, nil
}

func TestFit_WorstCaseWithLadders(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	id := trackctxtest.WorstCase(t, e.st, lt.FixedNow)
	e.b.UseM2b(ears.New(ears.Options{Store: e.st, Library: e.lib, Now: e.now}), fakeStrengths{})
	b, err := e.b.Build(context.Background(), agent, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	if n := utf16Len(string(raw)); n > trackctx.DefaultLimit || n != trackctx.Measure(&b) || n != b.Budget.Chars {
		t.Errorf("default: %d units (Measure %d, chars %d), want <= %d", n, trackctx.Measure(&b), b.Budget.Chars, trackctx.DefaultLimit)
	}
	b, err = e.b.Build(context.Background(), agent, id, []string{"lyrics"})
	if err != nil {
		t.Fatal(err)
	}
	if n := utf16Len(string(marshal(t, b))); n > trackctx.IncludeLimit {
		t.Errorf("include=lyrics: %d units, want <= %d", n, trackctx.IncludeLimit)
	}
}
