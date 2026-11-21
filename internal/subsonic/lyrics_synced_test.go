package subsonic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Invented Italian lines; the three sections give four lyric lines and two separators.
const syncedLyrics = "[Verse]\nla chiave del mare\nsotto la luna chiara\n[Chorus]\nnoi balliamo piano\n[Outro]\nsilenzio sul fiume"

type fakeTimes struct {
	starts []time.Duration
	ok     bool
	err    error
	got    auth.Principal
}

func (f *fakeTimes) LineStarts(_ context.Context, p auth.Principal, _ string) ([]time.Duration, bool, error) {
	f.got = p
	return f.starts, f.ok, f.err
}

func ms(v ...int) []time.Duration {
	out := make([]time.Duration, len(v))
	for i, n := range v {
		out[i] = time.Duration(n) * time.Millisecond
	}
	return out
}

func (x *s3rig) setLyrics(text string) {
	x.t.Helper()
	x.exec("DROP TRIGGER IF EXISTS recipes_immutable") // this test database is private
	x.exec(`UPDATE recipes SET lyrics = ? WHERE id = (SELECT recipe_id FROM tracks WHERE id = ?)`, text, luna)
}

func structured(t *testing.T, d map[string]any) map[string]any {
	t.Helper()
	sl := list(t, d, "lyricsList", "structuredLyrics")
	eq(t, "one entry", len(sl), 1)
	return sl[0]
}

// seedDiction stores the tier_b_raw and tier_b_diction rows a listening check would write for the take.
func (x *s3rig) seedDiction(trackID, lyrics string, o tierbtest.Opts) {
	x.t.Helper()
	raw, err := tierb.Parse(tierbtest.FromLyrics(lyrics, o))
	if err != nil {
		x.t.Fatal(err)
	}
	res := tierb.Diction(tierb.RefWords(lyrics), raw)
	type sec struct {
		Lines []tierb.LineResult `json:"lines"`
	}
	doc := struct {
		Sections []sec `json:"sections"`
	}{}
	for _, s := range res.Sections {
		doc.Sections = append(doc.Sections, sec{Lines: s.Lines})
	}
	b, _ := json.Marshal(doc)
	at := store.FormatTime(x.clk.now())
	x.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'tier_b_diction', 'mm-diction/1 go1 abc', 'B', ?, ?)`, trackID, string(b), at)
	x.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'tier_b_raw', 'mm-tierb-raw/1 x', 'B', '{"language":"it"}', ?)`, trackID, at)
}

func (x *s3rig) useRealDiction() {
	x.srv.o.Diction = diction.New(diction.Options{Store: x.st, Library: library.New(x.st, nil, x.clk.now), Now: x.clk.now})
}

func lineStartsOf(t *testing.T, sl map[string]any) (vals []string, starts []float64) {
	t.Helper()
	for _, l := range sl["line"].([]any) {
		m := l.(map[string]any)
		vals = append(vals, m["value"].(string))
		if s, ok := m["start"].(float64); ok {
			starts = append(starts, s)
		}
	}
	return vals, starts
}

func TestSubsonic_LyricsSyncedFromTierB(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.setLyrics(syncedLyrics)
	f := &fakeTimes{ok: true, starts: ms(10000, 14500, 20000, 31250)}
	x.srv.o.Diction = f
	sl := structured(t, x.call("getLyricsBySongId", "id", luna))
	eq(t, "synced", sl["synced"], true)
	vals, starts := lineStartsOf(t, sl)
	eq(t, "lines without separators", strings.Join(vals, "|"), "la chiave del mare|sotto la luna chiara|noi balliamo piano|silenzio sul fiume")
	eq(t, "starts", fmt.Sprint(starts), "[10000 14500 20000 31250]")
	if f.got.ID == "" {
		t.Errorf("the collaborator was not given the authenticated principal: %+v", f.got)
	}

	// 3 of 4 lines timed is exactly 75 %: synced; the untimed line takes the previous start plus 1 ms
	f.starts = ms(10000, -1, 20000, 31250)
	sl = structured(t, x.call("getLyricsBySongId", "id", luna))
	eq(t, "synced at 75 percent", sl["synced"], true)
	_, starts = lineStartsOf(t, sl)
	eq(t, "filled start", fmt.Sprint(starts), "[10000 10001 20000 31250]")

	// below 75 %: unsynced, no start anywhere
	f.starts = ms(10000, -1, -1, 31250)
	sl = structured(t, x.call("getLyricsBySongId", "id", luna))
	eq(t, "unsynced below 75 percent", sl["synced"], false)
	if _, starts = lineStartsOf(t, sl); len(starts) != 0 {
		t.Errorf("start on an unsynced answer: %v", starts)
	}

	// real service over real Tier B rows: starts are the heard times
	x.useRealDiction()
	x.seedDiction(luna, syncedLyrics, tierbtest.Opts{})
	sl = structured(t, x.call("getLyricsBySongId", "id", luna))
	eq(t, "synced from real rows", sl["synced"], true)
	vals, starts = lineStartsOf(t, sl)
	eq(t, "real lines", len(vals), 4)
	eq(t, "real starts", len(starts), 4)
	for i := 1; i < len(starts); i++ {
		if starts[i] <= starts[i-1] {
			t.Errorf("starts not increasing: %v", starts)
		}
	}
	if starts[0] < 9000 {
		t.Errorf("first start %v ms is before the first sung word", starts[0])
	}

	// the XML form carries start as an attribute
	w := x.get("getLyricsBySongId", x.rwq("id", luna, "f", "xml"), libIP)
	if body := w.Body.String(); !strings.Contains(body, `synced="true"`) || !strings.Contains(body, ` start="`) {
		t.Errorf("xml answer: %s", body)
	}
}

func TestSubsonic_LyricsUnsyncedWithoutTierB(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.setLyrics(syncedLyrics)
	body := func(f string) (string, string) {
		r := x.get("getLyricsBySongId", x.rwq("id", luna, "f", f), libIP)
		return r.Body.String(), r.Header().Get("Content-Type")
	}
	wantJSON, _ := body("json")
	wantXML, _ := body("xml")
	if strings.Contains(wantJSON, `"start"`) || strings.Contains(wantXML, "start=") {
		t.Fatalf("M4a answer carries a start: %s %s", wantJSON, wantXML)
	}
	eq(t, "unsynced", structured(t, x.call("getLyricsBySongId", "id", luna))["synced"], false)

	// the real service, a take with no Tier B rows: answers exactly the same bytes as without it
	x.useRealDiction()
	for _, c := range []struct{ f, want string }{{"json", wantJSON}, {"xml", wantXML}} {
		if got, _ := body(c.f); got != c.want {
			t.Errorf("%s answer changed without Tier B:\n got %s\nwant %s", c.f, got, c.want)
		}
	}
	// a collaborator that says no, that cannot see the track, and one with a mismatched count: the same bytes
	for name, f := range map[string]*fakeTimes{
		"not asr":   {ok: false, starts: ms(1, 2, 3, 4)},
		"not found": {err: library.ErrNotFound},
		"mismatch":  {ok: true, starts: ms(1, 2)},
	} {
		x.srv.o.Diction = f
		if got, _ := body("json"); got != wantJSON {
			t.Errorf("%s: answer changed:\n got %s\nwant %s", name, got, wantJSON)
		}
	}
	// a failing collaborator is an error, never silently synced
	x.srv.o.Diction = &fakeTimes{err: fmt.Errorf("boom")}
	eq(t, "failure", x.callCode("getLyricsBySongId", "id", luna), 0)
}

func TestSubsonic_ProportionalNeverSynced(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.setLyrics(syncedLyrics)
	x.useRealDiction()
	// a take with timed sections but no tier_b_diction row has the proportional basis only
	x.exec(`INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s) VALUES (?, 'plan', 9, 'verse', 'verse', '[Verse]', 5, 25)`, luna)
	sl := structured(t, x.call("getLyricsBySongId", "id", luna))
	eq(t, "synced", sl["synced"], false)
	if _, starts := lineStartsOf(t, sl); len(starts) != 0 {
		t.Errorf("proportional times leaked as starts: %v", starts)
	}
}

func TestSubsonic_LyricsSyncedHiddenTrack(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.setLyrics(syncedLyrics)
	x.useRealDiction()
	x.seedDiction(luna, syncedLyrics, tierbtest.Opts{})
	// the service itself refuses a viewer that hides discarded takes
	x.setStatus(luna, "discarded", x.clk.now())
	listener := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "bob", Name: "bob", User: "bob"}
	if _, _, err := x.srv.o.Diction.LineStarts(context.Background(), listener, luna); err == nil {
		t.Error("a listener got line starts of a discarded take")
	}
}
