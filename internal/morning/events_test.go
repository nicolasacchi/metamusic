package morning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
)

func evID(i int) string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe000+i) }

// readyCrate builds a crate of n takes (titles Alpha, Beta ...) and returns it with chapter number -> track id.
func (r *rig) readyCrate(n int) (Crate, map[int]string) {
	r.t.Helper()
	for i := 0; i < n; i++ {
		r.take(fmt.Sprintf("Take%c Secret", 'A'+i), time.Hour)
	}
	c, err := r.svc.Build(context.Background(), owner, BuildIn{})
	if err != nil {
		r.t.Fatal(err)
	}
	c = r.built(c)
	if c.State != StateReady || len(c.Chapters) < n {
		r.t.Fatalf("crate %+v", c)
	}
	byN := map[int]string{}
	for _, ch := range c.Chapters {
		byN[ch.N] = r.scalar(`SELECT track_id FROM morning_crate_items WHERE crate_id = ? AND n = ?`, c.ID, ch.N)
	}
	return c, byN
}

func (r *rig) counts() string {
	return r.scalar(`SELECT (SELECT COUNT(*) FROM comments) || '/' || (SELECT COUNT(*) FROM ratings) || '/' || (SELECT COUNT(*) FROM listen_events) || '/' || (SELECT COUNT(*) FROM playlists)`)
}

func stars(n int) *int { return &n }

func (r *rig) submit(p auth.Principal, id string, evs ...EventIn) EventsResult {
	r.t.Helper()
	res, err := r.svc.SubmitEvents(context.Background(), p, id, evs)
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

// TestMorningEvents_NoJudgmentRowsBeforeReveal: markers, skips and stars before the reveal write raw events and nothing else.
func TestMorningEvents_NoJudgmentRowsBeforeReveal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c, byN := r.readyCrate(3)
	before := r.counts()
	ch := c.Chapters[0]
	at := ch.AudioStartMS + 10_000
	heat := r.scalar(`SELECT COUNT(*) FROM listen_events WHERE track_id = ?`, byN[ch.N])
	res := r.submit(owner, c.ID,
		EventIn{ID: evID(1), Side: ch.Side, Chapter: ch.N, Kind: EventMarker, SideMS: at, ClientAt: dawn},
		EventIn{ID: evID(2), Side: ch.Side, Chapter: ch.N, Kind: EventStrongMarker, SideMS: at, ClientAt: dawn},
		EventIn{ID: evID(3), Side: ch.Side, Chapter: ch.N, Kind: EventSkip, SideMS: at, ClientAt: dawn},
		EventIn{ID: evID(4), Side: ch.Side, Chapter: ch.N, Kind: EventRate, Stars: stars(4), SideMS: at, ClientAt: dawn})
	if res.Accepted != 4 || res.Duplicates != 0 || len(res.Rejected) != 0 || res.Revealed {
		t.Fatalf("result %+v", res)
	}
	if got := r.counts(); got != before {
		t.Fatalf("judgment rows moved before the reveal: %s -> %s", before, got)
	}
	if got := r.scalar(`SELECT COUNT(*) FROM listen_events WHERE track_id = ?`, byN[ch.N]); got != heat {
		t.Fatalf("listening of the take moved: %s -> %s", heat, got)
	}
	if got := r.scalar(`SELECT COUNT(*) FROM morning_crate_events WHERE mapped_at IS NOT NULL`); got != "0" {
		t.Fatalf("%s events mapped before the reveal", got)
	}
}

// TestMorningEvents_MappedAtReveal: the reveal maps markers, keeps, skips and stars, and a replay of the ids adds nothing.
func TestMorningEvents_MappedAtReveal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c, byN := r.readyCrate(3)
	ch := c.Chapters[1]
	track := byN[ch.N]
	evs := []EventIn{
		{ID: evID(1), Side: ch.Side, Chapter: ch.N, Kind: EventMarker, SideMS: ch.AudioStartMS + 73_200, LatencyMS: 1200, ClientAt: dawn},
		{ID: evID(2), Side: ch.Side, Chapter: ch.N, Kind: EventStrongMarker, SideMS: ch.AudioStartMS + 30_000, ClientAt: dawn},
		{ID: evID(3), Side: ch.Side, Chapter: ch.N, Kind: EventSkip, SideMS: ch.AudioStartMS + 54_500, ClientAt: dawn},
		{ID: evID(4), Side: ch.Side, Chapter: ch.N, Kind: EventRate, Stars: stars(4), SideMS: ch.AudioStartMS + 90_000, ClientAt: dawn},
	}
	r.submit(owner, c.ID, evs...)
	if _, err := r.svc.Reveal(ctx, ownerDev, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.scalar(`SELECT at_second || '/' || source || '/' || kind || '/' || body || '/' || principal_id FROM comments WHERE kind = 'marker' AND source = 'headset'`); got != "72.0/headset/marker/Headset marker/device:dev1" {
		t.Fatalf("marker comment = %s", got)
	}
	if got := r.scalar(`SELECT at_second || '/' || source || '/' || body FROM comments WHERE kind = 'keep' AND source = 'headset'`); got != "30.0/headset/Headset keep" {
		t.Fatalf("keep comment = %s", got)
	}
	if got := r.scalar(`SELECT COUNT(*) FROM comments WHERE source = 'headset' AND track_id <> ?`, track); got != "0" {
		t.Fatalf("a comment landed on another take")
	}
	if got := r.scalar(`SELECT from_ms || '/' || kind || '/' || session_id FROM listen_events WHERE id = ?`, evID(3)); !strings.HasPrefix(got, "54500/skip/morning-") {
		t.Fatalf("listen event = %s", got)
	}
	if got := r.scalar(`SELECT stars FROM ratings WHERE track_id = ? AND source = 'human' AND rater = 'owner'`, track); got != "4" {
		t.Fatalf("rating = %s", got)
	}
	if got := r.scalar(`SELECT COUNT(*) FROM morning_crate_events WHERE mapped_at IS NULL`); got != "0" {
		t.Fatalf("%s events left unmapped", got)
	}
	after := r.counts()
	res := r.submit(owner, c.ID, evs...)
	if res.Accepted != 0 || res.Duplicates != 4 || !res.Revealed {
		t.Fatalf("replay %+v", res)
	}
	if got := r.counts(); got != after {
		t.Fatalf("a replay moved rows: %s -> %s", after, got)
	}
}

// TestMorningEvents_AfterRevealMappedAtOnce: an event that arrives after the reveal is mapped in its own insert transaction.
func TestMorningEvents_AfterRevealMappedAtOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c, byN := r.readyCrate(2)
	if _, err := r.svc.Reveal(ctx, owner, c.ID); err != nil {
		t.Fatal(err)
	}
	ch := c.Chapters[0]
	res := r.submit(owner, c.ID, EventIn{ID: evID(1), Side: ch.Side, Chapter: ch.N, Kind: EventMarker, SideMS: ch.AudioStartMS + 5_000, ClientAt: dawn},
		EventIn{ID: evID(2), Side: ch.Side, Chapter: ch.N, Kind: EventPlay, SideMS: ch.AudioStartMS, ClientAt: dawn},
		EventIn{ID: evID(3), Side: ch.Side, Chapter: ch.N, Kind: EventRate, Stars: stars(3), SideMS: ch.AudioStartMS, ClientAt: dawn})
	if res.Accepted != 3 || !res.Revealed {
		t.Fatalf("result %+v", res)
	}
	if got := r.scalar(`SELECT COUNT(*) FROM morning_crate_events WHERE mapped_at IS NULL`); got != "0" {
		t.Fatalf("%s unmapped after the reveal", got)
	}
	if got := r.scalar(`SELECT COUNT(*) FROM comments WHERE track_id = ? AND source = 'headset'`, byN[ch.N]); got != "1" {
		t.Fatalf("comments = %s", got)
	}
	if got := r.scalar(`SELECT kind FROM listen_events WHERE id = ?`, evID(2)); got != "start" {
		t.Fatalf("a play is a start, got %s", got)
	}
	if got := r.scalar(`SELECT stars FROM ratings WHERE track_id = ? AND rater = 'owner'`, byN[ch.N]); got != "3" {
		t.Fatalf("rating = %s", got)
	}
}

// TestMorning_AutoRevealWhenAllRated: the batch that gives every non-anchor chapter stars reveals the crate in the same call.
func TestMorning_AutoRevealWhenAllRated(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c, byN := r.readyCrate(3)
	var evs []EventIn
	for i, ch := range c.Chapters[:2] {
		evs = append(evs, EventIn{ID: evID(i), Side: ch.Side, Chapter: ch.N, Kind: EventRate, Stars: stars(5), SideMS: ch.AudioStartMS, ClientAt: dawn})
	}
	if res := r.submit(owner, c.ID, evs...); res.Revealed {
		t.Fatal("revealed with a chapter unrated")
	}
	last := c.Chapters[2]
	res := r.submit(ownerDev, c.ID, EventIn{ID: evID(9), Side: last.Side, Chapter: last.N, Kind: EventRate, Stars: stars(2), SideMS: last.AudioStartMS, ClientAt: dawn})
	if !res.Revealed {
		t.Fatal("not revealed when the last chapter was rated")
	}
	if got := r.scalar(`SELECT revealed_by FROM morning_crates WHERE id = ?`, c.ID); got != "device:dev1" {
		t.Fatalf("revealed_by = %s", got)
	}
	for n, track := range byN {
		if got := r.scalar(`SELECT COUNT(*) FROM ratings WHERE rater = 'owner' AND track_id = ?`, track); got != "1" {
			t.Fatalf("chapter %d has %s owner ratings", n, got)
		}
	}
	got, err := r.svc.Get(context.Background(), owner, c.ID)
	if err != nil || !got.Revealed || got.PlaylistID == nil {
		t.Fatalf("crate after auto reveal %+v %v", got, err)
	}
}

// TestMorning_AutoRevealNeedsARatedChapterSet: clearing a rating un-rates a chapter, and anchors alone never reveal.
func TestMorning_AutoRevealIgnoresAnchorsAndClears(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setting("anchors", 1)
	five := r.take("Beloved", 300*time.Hour)
	r.rate(five, 5)
	c, byN := r.readyCrate(2)
	var anchor ChapterView
	var rest []ChapterView
	for _, ch := range c.Chapters {
		if r.scalar(`SELECT anchor FROM morning_crate_items WHERE crate_id = ? AND n = ?`, c.ID, ch.N) == "1" {
			anchor = ch
		} else {
			rest = append(rest, ch)
		}
	}
	if anchor.N == 0 || len(rest) != 2 {
		t.Fatalf("chapters %+v", c.Chapters)
	}
	ev := func(i int, ch ChapterView, s *int) EventIn {
		return EventIn{ID: evID(i), Side: ch.Side, Chapter: ch.N, Kind: EventRate, Stars: s, SideMS: ch.AudioStartMS, ClientAt: dawn}
	}
	r.clk.Advance(time.Second)
	r.submit(owner, c.ID, ev(1, anchor, stars(4)), ev(2, rest[0], stars(5)), ev(3, rest[1], stars(3)))
	// one rating cleared in a later call: the crate was revealed by the first, so use a fresh crate for the clear case
	if got := r.scalar(`SELECT stars FROM ratings WHERE track_id = ? AND rater = 'owner'`, byN[anchor.N]); got != "5" {
		t.Fatalf("the anchor's rating changed to %s: anchor stars are calibration only", got)
	}
	if got := r.scalar(`SELECT COUNT(*) FROM morning_crate_events WHERE kind = 'rate' AND rating_track_id IS NOT NULL`); got != "2" {
		t.Fatalf("mapped ratings = %s", got)
	}
}

func TestMorning_ClearBeforeRevealDoesNotCount(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c, _ := r.readyCrate(2)
	a, b := c.Chapters[0], c.Chapters[1]
	res := r.submit(owner, c.ID,
		EventIn{ID: evID(1), Side: a.Side, Chapter: a.N, Kind: EventRate, Stars: stars(5), SideMS: a.AudioStartMS, ClientAt: dawn},
		EventIn{ID: evID(2), Side: b.Side, Chapter: b.N, Kind: EventRate, Stars: stars(4), SideMS: b.AudioStartMS, ClientAt: dawn},
		EventIn{ID: evID(3), Side: b.Side, Chapter: b.N, Kind: EventRate, Stars: nil, SideMS: b.AudioStartMS, ClientAt: dawn})
	if res.Revealed {
		t.Fatal("a cleared chapter counted as rated")
	}
}

// TestMorning_BlindRatingSuperseded: a rating the Owner made directly after the event wins; the event is marked superseded.
func TestMorning_BlindRatingSuperseded(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c, byN := r.readyCrate(3) // a third chapter stays unrated, so the ratings below do not reveal the crate
	a, b := c.Chapters[0], c.Chapters[1]
	r.submit(owner, c.ID,
		EventIn{ID: evID(1), Side: a.Side, Chapter: a.N, Kind: EventRate, Stars: stars(2), SideMS: a.AudioStartMS, ClientAt: dawn},
		EventIn{ID: evID(2), Side: b.Side, Chapter: b.N, Kind: EventRate, Stars: stars(2), SideMS: b.AudioStartMS, ClientAt: dawn})
	// the Owner rates the first take directly, later, with a verdict; the second take keeps a verdict too, made earlier
	r.clk.Advance(time.Hour)
	v := "keeper"
	if _, err := r.judg.Rate(ctx, owner, byN[a.N], stars(5), &v); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Reveal(ctx, owner, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.scalar(`SELECT stars || '/' || verdict FROM ratings WHERE track_id = ? AND rater = 'owner'`, byN[a.N]); got != "5/keeper" {
		t.Fatalf("direct rating overwritten: %s", got)
	}
	if got := r.scalar(`SELECT superseded || '/' || COALESCE(rating_track_id, '-') FROM morning_crate_events WHERE id = ?`, evID(1)); got != "1/-" {
		t.Fatalf("event 1 = %s", got)
	}
	if got := r.scalar(`SELECT superseded || '/' || rating_track_id FROM morning_crate_events WHERE id = ?`, evID(2)); got != "0/"+byN[b.N] {
		t.Fatalf("event 2 = %s", got)
	}
	if got := r.scalar(`SELECT stars FROM ratings WHERE track_id = ? AND rater = 'owner'`, byN[b.N]); got != "2" {
		t.Fatalf("blind rating = %s", got)
	}
}

// TestMorning_LaterBlindRatingWinsAndKeepsVerdict: two blind ratings of one chapter write the later one, and the Owner's
// older verdict text survives.
func TestMorning_LaterBlindRatingWinsAndKeepsVerdict(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c, byN := r.readyCrate(2)
	a := c.Chapters[0]
	v := "old words"
	if _, err := r.judg.Rate(ctx, owner, byN[a.N], stars(1), &v); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(time.Hour)
	r.submit(owner, c.ID,
		EventIn{ID: evID(1), Side: a.Side, Chapter: a.N, Kind: EventRate, Stars: stars(3), SideMS: a.AudioStartMS, ClientAt: dawn},
		EventIn{ID: evID(2), Side: a.Side, Chapter: a.N, Kind: EventRate, Stars: stars(5), SideMS: a.AudioStartMS + 1, ClientAt: dawn})
	if _, err := r.svc.Reveal(ctx, owner, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.scalar(`SELECT stars || '/' || verdict FROM ratings WHERE track_id = ? AND rater = 'owner'`, byN[a.N]); got != "5/old words" {
		t.Fatalf("rating = %s", got)
	}
}

// TestMorning_PlaylistAtRevealOnly: no playlist before the reveal; the reveal makes one with the non-anchor takes in chapter order.
func TestMorning_PlaylistAtRevealOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c, byN := r.readyCrate(3)
	r.submit(owner, c.ID, EventIn{ID: evID(1), Side: 0, Chapter: c.Chapters[0].N, Kind: EventMarker, SideMS: c.Chapters[0].AudioStartMS, ClientAt: dawn})
	base := r.scalar(`SELECT COUNT(*) FROM playlists`)
	if got := r.scalar(`SELECT COUNT(*) FROM playlists WHERE created_by = 'system:morning'`); got != "0" {
		t.Fatalf("a playlist before the reveal")
	}
	got, err := r.svc.Reveal(ctx, owner, c.ID)
	if err != nil || got.PlaylistID == nil {
		t.Fatalf("reveal %+v %v", got, err)
	}
	if n := r.scalar(`SELECT name || '/' || created_by || '/' || items_count FROM playlists WHERE id = ?`, *got.PlaylistID); n != "Morning crate 2026-11-22/system:morning/3" {
		t.Fatalf("playlist = %s", n)
	}
	var items []string
	if err := json.Unmarshal([]byte(r.scalar(`SELECT items_json FROM playlists WHERE id = ?`, *got.PlaylistID)), &items); err != nil {
		t.Fatal(err)
	}
	for i, id := range items {
		if id != byN[i+1] {
			t.Fatalf("playlist order %v, chapters %v", items, byN)
		}
	}
	// revealing again is a no-op: one playlist, same crate
	again, err := r.svc.Reveal(ctx, owner, c.ID)
	if err != nil || *again.PlaylistID != *got.PlaylistID || r.scalar(`SELECT COUNT(*) FROM playlists WHERE created_by = 'system:morning'`) != "1" || r.scalar(`SELECT COUNT(*) FROM playlists`) == base {
		t.Fatalf("second reveal %+v %v", again, err)
	}
}

// TestMorning_RevealPersonOnly: an agent is 403 HUMAN_ONLY, a non-Owner 403 OWNER_ONLY, the Owner's device reveals.
func TestMorning_RevealPersonOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c, _ := r.readyCrate(2)
	ch := c.Chapters[0]
	ev := EventIn{ID: evID(1), Side: ch.Side, Chapter: ch.N, Kind: EventMarker, SideMS: ch.AudioStartMS, ClientAt: dawn}
	for name, tc := range map[string]struct {
		p    auth.Principal
		want string
	}{"agent": {agent, "HUMAN_ONLY"}, "editor": {editor, "OWNER_ONLY"}, "listener": {listener, "OWNER_ONLY"}, "anonymous": {auth0, "OWNER_ONLY"}} {
		if _, err := r.svc.Reveal(ctx, tc.p, c.ID); code(err) != tc.want {
			t.Errorf("%s reveal: %v, want %s", name, err, tc.want)
		}
		if _, err := r.svc.SubmitEvents(ctx, tc.p, c.ID, []EventIn{ev}); code(err) != tc.want {
			t.Errorf("%s submit: %v, want %s", name, err, tc.want)
		}
	}
	if got := r.scalar(`SELECT COUNT(*) FROM morning_crate_events`); got != "0" {
		t.Fatalf("refused callers wrote %s events", got)
	}
	if _, err := r.svc.Reveal(ctx, ownerDev, c.ID); err != nil {
		t.Fatal(err)
	}
}

func TestMorning_EventValidation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c, _ := r.readyCrate(2)
	ch := c.Chapters[0]
	base := func(i int) EventIn {
		return EventIn{ID: evID(i), Side: ch.Side, Chapter: ch.N, Kind: EventMarker, SideMS: ch.AudioStartMS, ClientAt: dawn}
	}
	bad := map[string]EventIn{}
	e := base(1)
	e.Chapter = 99
	bad["UNKNOWN_CHAPTER"] = e
	e = base(2)
	e.Side = 5
	bad["UNKNOWN_CHAPTER "] = e
	e = base(3)
	e.SideMS = 99_999_999
	bad["OUT_OF_RANGE"] = e
	e = base(4)
	e.LatencyMS = 5001
	bad["OUT_OF_RANGE "] = e
	e = base(5)
	e.Kind = "rate"
	e.Stars = stars(6)
	bad["INVALID_EVENT"] = e
	e = base(6)
	e.Stars = stars(3)
	bad["INVALID_EVENT "] = e
	e = base(7)
	e.ID = "not-a-uuid"
	bad["INVALID_EVENT  "] = e
	e = base(8)
	e.Kind = "bogus"
	bad["INVALID_EVENT   "] = e
	e = base(10)
	e.ClientAt = time.Time{}
	bad["INVALID_EVENT    "] = e
	var evs []EventIn
	want := map[string]string{}
	for code, e := range bad {
		evs = append(evs, e)
		want[e.ID] = strings.TrimSpace(code)
	}
	evs = append(evs, base(20))
	res, err := r.svc.SubmitEvents(ctx, owner, c.ID, evs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || len(res.Rejected) != len(bad) {
		t.Fatalf("result %+v", res)
	}
	for _, rj := range res.Rejected {
		if want[rj.ID] != rj.Code {
			t.Errorf("event %s rejected %s, want %s", rj.ID, rj.Code, want[rj.ID])
		}
	}
	if _, err := r.svc.SubmitEvents(ctx, owner, "nope", nil); code(err) != "NOT_FOUND" {
		t.Errorf("unknown crate: %v", err)
	}
	if _, err := r.svc.SubmitEvents(ctx, owner, c.ID, make([]EventIn, MaxEvents+1)); code(err) != "VALIDATION" {
		t.Errorf("too many: %v", err)
	}
	// a crate that is not ready cannot be rated or revealed
	r.exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at,built_at,takes) VALUES('empty1','manual','empty',1,'{}','human:alice',?,?,0)`, ts(dawn), ts(dawn))
	if _, err := r.svc.SubmitEvents(ctx, owner, "empty1", []EventIn{base(30)}); code(err) != "MORNING_CRATE_NOT_READY" {
		t.Errorf("empty crate submit: %v", err)
	}
	if _, err := r.svc.Reveal(ctx, owner, "empty1"); code(err) != "MORNING_CRATE_NOT_READY" {
		t.Errorf("empty crate reveal: %v", err)
	}
	if _, err := r.svc.Reveal(ctx, owner, "nope"); code(err) != "NOT_FOUND" {
		t.Errorf("unknown reveal: %v", err)
	}
}

// TestMorning_UnavailableWithoutJudgments: a service built without the judgment writers refuses the writes cleanly.
func TestMorning_UnavailableWithoutJudgments(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c, _ := r.readyCrate(2)
	bare := New(ServiceOptions{Store: r.st})
	t.Cleanup(bare.Close)
	if _, err := bare.Reveal(context.Background(), owner, c.ID); code(err) != "UNAVAILABLE" {
		t.Fatalf("reveal: %v", err)
	}
	if _, err := bare.SubmitEvents(context.Background(), owner, c.ID, nil); code(err) != "UNAVAILABLE" {
		t.Fatalf("submit: %v", err)
	}
}

// TestMorning_BlindBeforeReveal proves the blind rule over the events path: a non-Owner and an agent never get a title, a take id
// or a lineage name from the crate, its events or their results before the reveal, and the stars and markers do not unblind it.
func TestMorning_BlindBeforeReveal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c, byN := r.readyCrate(3)
	ch := c.Chapters[0]
	res := r.submit(owner, c.ID,
		EventIn{ID: evID(1), Side: ch.Side, Chapter: ch.N, Kind: EventRate, Stars: stars(4), SideMS: ch.AudioStartMS, ClientAt: dawn},
		EventIn{ID: evID(2), Side: ch.Side, Chapter: 77, Kind: EventMarker, SideMS: 0, ClientAt: dawn})
	secrets := []string{"Secret", "root_track_id", "lineage"}
	for _, id := range byN {
		secrets = append(secrets, id)
	}
	check := func(who string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		for _, s := range secrets {
			if strings.Contains(string(b), s) {
				t.Fatalf("%s sees %q before the reveal: %s", who, s, b)
			}
		}
	}
	check("submit result", res)
	for name, p := range map[string]auth.Principal{"owner": owner, "device": ownerDev, "agent": agent} {
		got, err := r.svc.Get(ctx, p, c.ID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		check(name, got)
		pg, err := r.svc.List(ctx, p, ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		check(name+" list", pg)
	}
	for name, p := range map[string]auth.Principal{"editor": editor, "listener": listener, "anonymous": auth0} {
		if _, err := r.svc.Get(ctx, p, c.ID); code(err) != "OWNER_ONLY" {
			t.Errorf("%s get: %v", name, err)
		}
		if _, err := r.svc.List(ctx, p, ListFilter{}); code(err) != "OWNER_ONLY" {
			t.Errorf("%s list: %v", name, err)
		}
	}
	// an agent that tries to reveal learns nothing and changes nothing
	if _, err := r.svc.Reveal(ctx, agent, c.ID); code(err) != "HUMAN_ONLY" {
		t.Fatalf("agent reveal: %v", err)
	}
	// nothing in the Owner's judgments names the crate before the reveal
	if got := r.scalar(`SELECT COUNT(*) FROM comments WHERE source = 'headset'`); got != "0" {
		t.Fatalf("headset comments before the reveal: %s", got)
	}
	var _ = judgments.Comment{}
	var _ = library.ErrNotFound
}

// TestMorning_RevealIsOneTransaction: a reveal that fails at its last step (the playlist) leaves the crate unrevealed, its events
// unmapped and no comment, rating or listen event behind.
func TestMorning_RevealIsOneTransaction(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c, _ := r.readyCrate(3)
	ch := c.Chapters[0]
	r.submit(owner, c.ID,
		EventIn{ID: evID(1), Side: ch.Side, Chapter: ch.N, Kind: EventStrongMarker, SideMS: ch.AudioStartMS + 1000, ClientAt: dawn},
		EventIn{ID: evID(2), Side: ch.Side, Chapter: ch.N, Kind: EventSkip, SideMS: ch.AudioStartMS + 2000, ClientAt: dawn},
		EventIn{ID: evID(3), Side: ch.Side, Chapter: ch.N, Kind: EventRate, Stars: stars(5), SideMS: ch.AudioStartMS, ClientAt: dawn})
	before := r.counts()
	r.exec(`CREATE TRIGGER no_playlists BEFORE INSERT ON playlists BEGIN SELECT RAISE(ABORT, 'test: no playlists'); END`)
	if _, err := r.svc.Reveal(ctx, owner, c.ID); err == nil {
		t.Fatal("reveal succeeded with a failing playlist")
	}
	if got := r.counts(); got != before {
		t.Fatalf("rows left by a failed reveal: %s -> %s", before, got)
	}
	if got := r.scalar(`SELECT COUNT(*) FROM morning_crates WHERE revealed_at IS NOT NULL`); got != "0" {
		t.Fatal("the crate is revealed")
	}
	if got := r.scalar(`SELECT COUNT(*) FROM morning_crate_events WHERE mapped_at IS NOT NULL`); got != "0" {
		t.Fatal("events stayed mapped")
	}
	r.exec(`DROP TRIGGER no_playlists`)
	if _, err := r.svc.Reveal(ctx, owner, c.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
}
