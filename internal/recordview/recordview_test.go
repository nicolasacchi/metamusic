package recordview_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

var human = auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin}, Via: "local"}

const (
	r1Title = "Synthetic Record One"
	canary1 = "Synthetic Curation Muse 1"
	canary2 = "Synthetic Curation Muse 2"
)

func newRig(t *testing.T) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	r.Settle(t)
	return r
}

func idOf(t *testing.T, r *servertest.Rig, table, title string) string {
	t.Helper()
	var id string
	if err := r.Store.R.QueryRow(`SELECT id FROM `+table+` WHERE title = ?`, title).Scan(&id); err != nil {
		t.Fatalf("%s %q: %v", table, title, err)
	}
	return id
}

func setFlag(t *testing.T, r *servertest.Rig, on bool) {
	t.Helper()
	v := "false"
	if on {
		v = "true"
	}
	if _, err := r.Store.W.Exec(`INSERT INTO settings(key, value) VALUES('curation.export_muse_names', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, v); err != nil {
		t.Fatal(err)
	}
}

// TestRecordView_Get: the slots of R1 resolve as the fixture says, the plan gain comes from the automatic target, the totals and
// warnings agree with the slots, and an empty Include returns none of the optional parts.
func TestRecordView_Get(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := idOf(t, r, "records", r1Title)
	v, err := r.Services.RecordView.Get(context.Background(), human, id, recordview.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Liner != nil || v.Energy != nil || v.Changes != nil || v.Tapes != nil || v.Preview != nil {
		t.Errorf("an empty Include returned optional parts: %+v", v)
	}
	if len(v.Slots) != 5 {
		t.Fatalf("slots = %d", len(v.Slots))
	}
	wantGain := []float64{1, -1, -2.2}
	for i, w := range wantGain {
		if g := v.Slots[i].GainDB; g == nil || *g != w {
			t.Errorf("slot %d gain = %v, want %v", i+1, g, w)
		}
	}
	if v.Slots[3].GainDB != nil || v.Slots[3].State != "empty" || v.Slots[3].Take != nil {
		t.Errorf("empty slot: %+v", v.Slots[3])
	}
	if v.Slots[2].Mode != "pinned" || !v.Slots[2].PinDiffers || v.Slots[2].Pick == nil || v.Slots[2].Pick.Source != "stars" {
		t.Errorf("pinned slot: %+v", v.Slots[2])
	}
	if v.Totals.Slots != 5 || v.Totals.Playable != 4 || v.Totals.Lossy != 1 || v.Totals.Warnings != len(v.Warnings) || v.Totals.Undecided != 2 {
		t.Errorf("totals: %+v warnings %v", v.Totals, v.Warnings)
	}
	if v.Cover.URL512 == "" || v.Cover.URL1024 == "" {
		t.Errorf("cover: %+v", v.Cover)
	}
	// a Record that is not published reports no undecided_pick warning (the list summaries agree)
	r2 := idOf(t, r, "records", "Synthetic Record Two")
	v2, err := r.Services.RecordView.Get(context.Background(), human, r2, recordview.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range v2.Warnings {
		if w.Code == "undecided_pick" {
			t.Errorf("draft record warns undecided_pick: %+v", v2.Warnings)
		}
	}
}

// TestRecordView_Pending: false while the changelog has caught up, true while a Pick event of a floating slot's Song is
// unconsumed, and true while one of its Takes is in the provenance queue.
func TestRecordView_Pending(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	id := idOf(t, r, "records", r1Title)
	pending := func() bool {
		v, err := r.Services.RecordView.Get(ctx, human, id, recordview.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return v.Pending
	}
	if pending() {
		t.Fatal("pending right after Settle")
	}
	var take string
	if err := r.Store.R.QueryRow(`SELECT take_id FROM song_picks WHERE song_id = (SELECT id FROM songs WHERE title = 'Synthetic Song S3')`).Scan(&take); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.W.Exec(`INSERT INTO provenance_dirty(kind, id, at) VALUES('track', ?, '2026-10-06T10:00:00.000Z')`, take); err != nil {
		t.Fatal(err)
	}
	if !pending() {
		t.Error("not pending with a Take of a slot Song in the queue")
	}
	if _, err := r.Store.W.Exec(`DELETE FROM provenance_dirty`); err != nil {
		t.Fatal(err)
	}
	// a human pin of a Song's older Take is a Pick event the floating slot has not consumed
	human := r.Human(t)
	b2 := idOf(t, r, "tracks", "Synthetic Take B2")
	song := idOf(t, r, "songs", "Synthetic Song S3")
	if resp := human.Do(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": b2}); resp.Status != 200 {
		t.Fatalf("pin: %d %s", resp.Status, resp.Body)
	}
	if !pending() {
		t.Error("not pending with an unconsumed Pick event")
	}
	r.Settle(t) // provenance, then the real Syncer: the event becomes a changelog row
	if pending() {
		t.Error("still pending after Settle")
	}
	page, err := r.Services.Records.Changes(ctx, records.ChangeFilter{RecordID: id, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Kind != "slot_upgraded" {
		t.Errorf("newest change after Settle: %+v %v", page.Items, err)
	}
}

// TestRecordView_LinerAudience: the liner of get_record is the app audience: the Muse line is present, and costs are present for
// a Take a job made.
func TestRecordView_LinerAudience(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	id := idOf(t, r, "records", r1Title)
	v, err := r.Services.RecordView.Get(ctx, human, id, recordview.GetOptions{Include: []string{recordview.IncludeLiner}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Liner == nil || !strings.Contains(v.Liner.Markdown, "inspired by "+canary1) || !strings.Contains(v.Liner.Markdown, "inspired by "+canary2) {
		t.Fatalf("liner lacks the Muse lines: %+v", v.Liner)
	}
	if len(v.Liner.Markdown) > 8*1024 {
		t.Errorf("liner is %d bytes", len(v.Liner.Markdown))
	}
	// a Record holding a Take made by a job shows its cost
	var luna, child string
	if err := r.Store.R.QueryRow(`SELECT s.id, t.id FROM tracks t JOIN take_songs ts ON ts.track_id = t.id JOIN songs s ON s.id = ts.song_id WHERE t.job_id IS NOT NULL LIMIT 1`).Scan(&luna, &child); err != nil {
		t.Fatalf("no job-made take in the fixture: %v", err)
	}
	rec, err := r.Services.Records.Create(ctx, human, recordsNew("Synthetic Cost Record", luna))
	if err != nil {
		t.Fatal(err)
	}
	slots, err := r.Services.Records.Slots(ctx, rec.ID)
	if err != nil || len(slots) != 1 {
		t.Fatalf("slots: %v %v", slots, err)
	}
	if _, err := r.Services.Records.EditSlots(ctx, human, rec.ID, rec.Revision, []records.SlotOp{{Op: "pin", SlotID: slots[0].ID, TakeID: child}}); err != nil {
		t.Fatal(err)
	}
	cv, err := r.Services.RecordView.Get(ctx, human, rec.ID, recordview.GetOptions{Include: []string{recordview.IncludeLiner}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cv.Liner.Markdown, "Total cost: $") {
		t.Errorf("app liner without costs:\n%s", cv.Liner.Markdown)
	}
	// the export-audience preview never shows cost
	pv, err := r.Services.RecordView.Get(ctx, human, rec.ID, recordview.GetOptions{Include: []string{recordview.IncludePreview}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range pv.Preview.Tags {
		if strings.Contains(tg.Value, "cost") || strings.Contains(tg.Value, "$") {
			t.Errorf("preview tag %s carries cost: %q", tg.Key, tg.Value)
		}
	}
}

// TestRecordView_PickChangeKeepsCoverKey: a Pick flip changes slots[].take but not the cover key; adding a slot does.
func TestRecordView_PickChangeKeepsCoverKey(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	id := idOf(t, r, "records", r1Title)
	key := func() string {
		in, err := r.Services.RecordView.CoverInput(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return cover.MosaicKey(in, 512)
	}
	take := func() string {
		v, err := r.Services.RecordView.Get(ctx, human, id, recordview.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return v.Slots[1].Take.ID
	}
	before, k0 := take(), key()
	hc := r.Human(t)
	b2, s3 := idOf(t, r, "tracks", "Synthetic Take B2"), idOf(t, r, "songs", "Synthetic Song S3")
	if resp := hc.Do(t, "PUT", "/api/songs/"+s3+"/pin", map[string]any{"take_id": b2}); resp.Status != 200 {
		t.Fatalf("pin: %d %s", resp.Status, resp.Body)
	}
	if after := take(); after == before {
		t.Fatalf("the Pick flip did not change the slot's Take (%s)", after)
	}
	if k1 := key(); k1 != k0 {
		t.Errorf("a Pick change moved the cover key: %s -> %s", k0, k1)
	}
	if resp := hc.Do(t, "POST", "/api/records/"+id+"/slots", map[string]any{"revision": 3, "ops": []any{map[string]any{"op": "add", "song_id": idOf(t, r, "songs", "Synthetic Song S1")}}}); resp.Status != 200 {
		t.Fatalf("add: %d %s", resp.Status, resp.Body)
	}
	if k2 := key(); k2 == k0 {
		t.Error("adding a slot left the cover key unchanged")
	}
}

// TestRecordView_CoverUsesFirstTwelveParts: only the first records.CoverParts slot Songs feed the cover input.
func TestRecordView_CoverUsesFirstTwelveParts(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	in, err := r.Services.RecordView.CoverInput(context.Background(), idOf(t, r, "records", r1Title))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != cover.KindRecord || len(in.Parts) != 5 || in.Parts[0].RootID == "" || in.Seed == 0 {
		t.Errorf("cover input: %+v", in)
	}
}

// TestRecordView_Snapshot: the tape snapshot of the published R1: resolved Takes in order, the empty slot skipped, measures,
// masters, the tag set, the description of the export liner and the cover input.
func TestRecordView_Snapshot(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := idOf(t, r, "records", r1Title)
	s, err := r.Services.RecordView.Snapshot(context.Background(), id, tape.SnapOptions{IncludeLiner: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.RecordID != id || s.Status != "published" || s.Revision != 3 || s.Title != r1Title || s.MuseNames {
		t.Errorf("header: %+v", s)
	}
	var titles []string
	for _, it := range s.Items {
		titles = append(titles, it.Title)
		if !it.Measure.OK || it.MasterRel == "" || it.MasterSHA256 == "" || it.SampleRate == 0 || it.Bits == 0 || it.DurationS <= 0 {
			t.Errorf("item %s incomplete: %+v", it.Title, it)
		}
	}
	if want := []string{"Synthetic Take A1", "Synthetic Take B1", "Synthetic Take C2", "Synthetic Take E1"}; !slices.Equal(titles, want) {
		t.Errorf("items = %v, want %v (the empty slot is skipped, a pin beats the Pick)", titles, want)
	}
	if s.Items[3].Position != 5 || !s.Items[3].Lossy {
		t.Errorf("item 4: %+v", s.Items[3])
	}
	keys := map[string]string{}
	for _, c := range s.Tags {
		keys[c.Key] = c.Value
	}
	if keys["TITLE"] != r1Title || keys["ARTIST"] != "metamusic" || keys["DATE"] != "2026-10-06" || keys["GENRE"] != "dance, reggae" || keys["LINER_NOTES"] == "" {
		t.Errorf("tags: %v", keys)
	}
	if s.Description == "" || strings.Contains(s.Description, "$") {
		t.Errorf("description: %q", s.Description)
	}
	if s.CoverInput.Kind != cover.KindRecord || s.CoverInput.ID != id {
		t.Errorf("cover input: %+v", s.CoverInput)
	}
	if len(s.Warnings) != 2 || s.Warnings[0].Code != "undecided_pick" {
		t.Errorf("warnings: %+v", s.Warnings)
	}
	if none, err := r.Services.RecordView.Snapshot(context.Background(), id, tape.SnapOptions{}); err != nil || none.Description != "" {
		t.Errorf("without the liner: %q, %v", none.Description, err)
	}
}

// TestRecordView_SnapshotFlag: a canary Muse appears in the snapshot only with curation.export_muse_names on.
func TestRecordView_SnapshotFlag(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := idOf(t, r, "records", r1Title)
	dump := func() (string, bool) {
		s, err := r.Services.RecordView.Snapshot(context.Background(), id, tape.SnapOptions{IncludeLiner: true})
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, c := range s.Tags {
			b.WriteString(c.Key + "=" + c.Value + "\n")
		}
		b.WriteString(s.Description)
		for _, it := range s.Items {
			b.WriteString(it.Title)
		}
		return b.String(), s.MuseNames
	}
	off, flagOff := dump()
	if flagOff || strings.Contains(off, canary1) || strings.Contains(off, canary2) || strings.Contains(off, "Synthetic Curation Artist") {
		t.Errorf("Muse text without the flag (MuseNames=%v): %s", flagOff, off)
	}
	setFlag(t, r, true)
	on, flagOn := dump()
	if !flagOn || !strings.Contains(on, canary2) {
		t.Errorf("flag on (MuseNames=%v) but no Muse line: %s", flagOn, on)
	}
}

// TestRecordView_Preview: the preview's chapters, tags, description and warnings equal what Snapshot builds, and nothing is
// written (no cover file, no tape row).
func TestRecordView_Preview(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	id := idOf(t, r, "records", r1Title)
	count := func() int {
		var n int
		if err := r.Store.R.QueryRow(`SELECT (SELECT COUNT(*) FROM tapes) + (SELECT COUNT(*) FROM record_changes) + (SELECT COUNT(*) FROM audit_log)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	v, err := r.Services.RecordView.Get(ctx, human, id, recordview.GetOptions{Include: []string{recordview.IncludePreview}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Services.RecordView.Snapshot(ctx, id, tape.SnapOptions{IncludeLiner: true})
	if err != nil {
		t.Fatal(err)
	}
	p := v.Preview
	if p == nil || len(p.Chapters) != len(s.Items) || len(p.Tags) != len(s.Tags) || p.Description != s.Description || len(p.Warnings) != len(s.Warnings) {
		t.Fatalf("preview %+v vs snapshot %d items %d tags", p, len(s.Items), len(s.Tags))
	}
	for i, it := range s.Items {
		if p.Chapters[i].Position != it.Position || !strings.HasPrefix(p.Chapters[i].Name, it.Title) {
			t.Errorf("chapter %d = %+v, item %+v", i, p.Chapters[i], it)
		}
	}
	for i, c := range s.Tags {
		if p.Tags[i].Key != c.Key || p.Tags[i].Value != c.Value {
			t.Errorf("tag %d = %+v, want %+v", i, p.Tags[i], c)
		}
	}
	if count() != before {
		t.Error("building the preview wrote rows")
	}
}

// TestRecordView_NotFound: an unknown Record is the 404 of the records service, on every read.
func TestRecordView_NotFound(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.Services.RecordView.Get(ctx, human, "nope", recordview.GetOptions{}); err == nil {
		t.Error("Get of an unknown Record succeeded")
	}
	if _, err := r.Services.RecordView.Snapshot(ctx, "nope", tape.SnapOptions{}); err == nil {
		t.Error("Snapshot of an unknown Record succeeded")
	}
	if _, err := r.Services.RecordView.CoverInput(ctx, "nope"); err == nil {
		t.Error("CoverInput of an unknown Record succeeded")
	}
}
