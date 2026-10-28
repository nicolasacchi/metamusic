package records_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

type textRow struct {
	Name   string            `json:"name"`
	Change records.Change    `json:"change"`
	Names  map[string]string `json:"names"`
	Text   string            `json:"text"`
}

func ip(n int) *int { return &n }

func j(s string) json.RawMessage { return json.RawMessage(s) }

const (
	evLadder1 = `{"def":"mm.pick/1","rule":"ladder","eligible":2,"duels_n":1}`
	evLadder4 = `{"def":"mm.pick/1","rule":"ladder","eligible":3,"duels_n":4}`
	evStars   = `{"def":"mm.pick/1","rule":"stars","eligible":2,"stars":4,"tied":1}`
	evStars3  = `{"def":"mm.pick/1","rule":"stars","eligible":4,"stars":5,"tied":3}`
	evNewest  = `{"def":"mm.pick/1","rule":"newest","eligible":2}`
)

// titles are synthetic take titles; the rows themselves carry take ids only.
var titles = map[string]string{"tk-a": "Synthetic Take A", "tk-b": "Synthetic Take B"}

// textCases is the C122.3 table: one row per kind (20) plus the four why-text variants of slot_upgraded.
func textCases() []textRow {
	ch := func(kind string, pos *int, from, to, reason *string, evidence, detail json.RawMessage) records.Change {
		return records.Change{ID: "ch-" + kind, Seq: 2, At: "2026-10-15T12:00:00.000Z", Kind: kind, Position: pos,
			FromTakeID: from, ToTakeID: to, Reason: reason, Evidence: evidence, Detail: detail,
			Actor: "human:h", ActorKind: "human", ActorName: "h"}
	}
	slot := func(c records.Change) records.Change { c.SlotID = sp("sl-1"); return c }
	a, b := sp("tk-a"), sp("tk-b")
	return []textRow{
		{Name: "created", Change: ch("created", nil, nil, nil, nil, nil, nil)},
		{Name: "renamed", Change: ch("renamed", nil, nil, nil, nil, nil, nil)},
		{Name: "retexted", Change: ch("retexted", nil, nil, nil, nil, nil, nil)},
		{Name: "cover_rerolled", Change: ch("cover_rerolled", nil, nil, nil, nil, nil, nil)},
		{Name: "hidden", Change: ch("hidden", nil, nil, nil, nil, nil, nil)},
		{Name: "unhidden", Change: ch("unhidden", nil, nil, nil, nil, nil, nil)},
		{Name: "proposed", Change: ch("proposed", nil, nil, nil, nil, nil, nil)},
		{Name: "proposal_dismissed", Change: ch("proposal_dismissed", nil, nil, nil, nil, nil, j(`{"reason":"edited"}`))},
		{Name: "published", Change: ch("published", nil, nil, nil, nil, nil, nil)},
		{Name: "unpublished", Change: ch("unpublished", nil, nil, nil, nil, nil, nil)},
		{Name: "slot_added", Change: slot(ch("slot_added", ip(3), nil, a, sp("initial"), nil, nil))},
		{Name: "slot_removed", Change: slot(ch("slot_removed", ip(2), a, nil, nil, nil, nil))},
		{Name: "slot_moved", Change: slot(ch("slot_moved", ip(1), nil, nil, nil, nil, j(`{"from":4}`)))},
		{Name: "slot_side", Change: slot(ch("slot_side", ip(2), nil, nil, nil, nil, j(`{"side":"B"}`)))},
		{Name: "slot_pinned", Change: slot(ch("slot_pinned", ip(3), a, b, sp("pin"), nil, nil))},
		{Name: "slot_unpinned", Change: slot(ch("slot_unpinned", ip(3), b, a, sp("unpin"), nil, nil))},
		{Name: "slot_filled", Change: slot(ch("slot_filled", ip(5), nil, a, sp("ladder"), j(evLadder4), nil))},
		{Name: "slot_emptied", Change: slot(ch("slot_emptied", ip(5), a, nil, sp("emptied"), j(evNewest), nil))},
		{Name: "slot_song_followed", Change: slot(ch("slot_song_followed", ip(2), a, b, sp("retired"), nil, nil))},
		{Name: "slot_upgraded, ladder 4 duels", Change: slot(ch("slot_upgraded", ip(1), a, b, sp("ladder"), j(evLadder4), nil))},
		{Name: "slot_upgraded, ladder 1 duel", Change: slot(ch("slot_upgraded", ip(1), a, b, sp("ladder"), j(evLadder1), nil))},
		{Name: "slot_upgraded, stars tied 3", Change: slot(ch("slot_upgraded", ip(1), a, b, sp("stars"), j(evStars3), j(`{"undecided":true}`)))},
		{Name: "slot_upgraded, stars untied", Change: slot(ch("slot_upgraded", ip(1), a, b, sp("stars"), j(evStars), j(`{"undecided":true}`)))},
		{Name: "slot_upgraded, evidence missing", Change: slot(ch("slot_upgraded", ip(1), a, b, sp("ladder"), nil, nil))},
	}
}

// TestChangeText_Golden pins the 24 row texts (the web mirror reads the same table).
func TestChangeText_Golden(t *testing.T) {
	t.Parallel()
	rows := textCases()
	if len(rows) != 24 {
		t.Fatalf("%d rows, want 24 (20 kinds and 4 more why variants of slot_upgraded)", len(rows))
	}
	for i := range rows {
		rows[i].Names = titles
		rows[i].Text = records.ChangeText(rows[i].Change, func(id string) string { return titles[id] })
	}
	golden(t, "change_text.golden.json", rows)
}

func TestChangeText_Texts(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"created": "Record created", "renamed": "Title changed", "retexted": "Notes changed", "cover_rerolled": "Cover re-rolled",
		"hidden": "Hidden", "unhidden": "Unhidden", "proposed": "Proposed for publishing", "proposal_dismissed": "Sent back to draft",
		"published": "Published", "unpublished": "Unpublished",
		"slot_added":                      "Slot 3 added: Synthetic Take A",
		"slot_removed":                    "Slot 2 removed (Synthetic Take A)",
		"slot_moved":                      "Slot moved 4 → 1",
		"slot_side":                       "Slot 2 side B",
		"slot_pinned":                     "Slot 3 pinned to Synthetic Take B",
		"slot_unpinned":                   "Slot 3 unpinned: follows the Pick (Synthetic Take A)",
		"slot_filled":                     "Slot 5 filled: Synthetic Take A (won 4 blind duels)",
		"slot_emptied":                    "Slot 5 emptied: no playable take (was Synthetic Take A)",
		"slot_song_followed":              "Slot 2 follows the merged song",
		"slot_upgraded, ladder 4 duels":   "Slot 1 upgraded: Synthetic Take A → Synthetic Take B (won 4 blind duels)",
		"slot_upgraded, ladder 1 duel":    "Slot 1 upgraded: Synthetic Take A → Synthetic Take B (won 1 blind duel)",
		"slot_upgraded, stars tied 3":     "Slot 1 upgraded: Synthetic Take A → Synthetic Take B (5★ (newest of 3 tied) · undecided)",
		"slot_upgraded, stars untied":     "Slot 1 upgraded: Synthetic Take A → Synthetic Take B (4★ · undecided)",
		"slot_upgraded, evidence missing": "Slot 1 upgraded: Synthetic Take A → Synthetic Take B (won blind duels)",
	}
	for _, r := range textCases() {
		got := records.ChangeText(r.Change, func(id string) string { return titles[id] })
		if got != want[r.Name] {
			t.Errorf("%s: %q, want %q", r.Name, got, want[r.Name])
		}
	}
}

// TestChangeText_Fallbacks: a missing position, lookup, side, from-position or reason renders the generic form, never an error.
func TestChangeText_Fallbacks(t *testing.T) {
	t.Parallel()
	named := func(id string) string { return titles[id] }
	for _, c := range []struct {
		name string
		c    records.Change
		n    func(string) string
		want string
	}{
		{"no names func", records.Change{Kind: "slot_pinned", Position: ip(1), ToTakeID: sp("tk-a")}, nil, "Slot 1 pinned to a take"},
		{"unknown take", records.Change{Kind: "slot_pinned", Position: ip(1), ToTakeID: sp("tk-zz")}, named, "Slot 1 pinned to a take"},
		{"no position", records.Change{Kind: "slot_upgraded", FromTakeID: sp("tk-a"), ToTakeID: sp("tk-b")}, named, "Slot upgraded: Synthetic Take A → Synthetic Take B"},
		{"upgrade without a reason", records.Change{Kind: "slot_upgraded", Position: ip(2), FromTakeID: sp("tk-a"), ToTakeID: sp("tk-b")}, named, "Slot 2 upgraded: Synthetic Take A → Synthetic Take B"},
		{"unknown reason", records.Change{Kind: "slot_upgraded", Position: ip(2), FromTakeID: sp("tk-a"), ToTakeID: sp("tk-b"), Reason: sp("whim")}, named, "Slot 2 upgraded: Synthetic Take A → Synthetic Take B"},
		{"moved without detail", records.Change{Kind: "slot_moved", Position: ip(3)}, named, "Slot moved to 3"},
		{"moved with broken detail", records.Change{Kind: "slot_moved", Position: ip(3), Detail: j(`[`)}, named, "Slot moved to 3"},
		{"side cleared", records.Change{Kind: "slot_side", Position: ip(1), Detail: j(`{"side":null}`)}, named, "Slot 1 side cleared"},
		{"side without detail", records.Change{Kind: "slot_side", Position: ip(1)}, named, "Slot 1 side changed"},
		{"removed an empty slot", records.Change{Kind: "slot_removed", Position: ip(4)}, named, "Slot 4 removed"},
		{"unpinned to nothing", records.Change{Kind: "slot_unpinned", Position: ip(1), FromTakeID: sp("tk-a")}, named, "Slot 1 unpinned: follows the Pick (no take yet)"},
		{"emptied without a from", records.Change{Kind: "slot_emptied", Position: ip(1)}, named, "Slot 1 emptied: no playable take"},
		{"filled without a reason", records.Change{Kind: "slot_filled", Position: ip(1), ToTakeID: sp("tk-a")}, named, "Slot 1 filled: Synthetic Take A"},
		{"added with no take yet", records.Change{Kind: "slot_added", Position: ip(4)}, named, "Slot 4 added (no take yet)"},
		{"unknown kind", records.Change{Kind: "slot_exploded", Position: ip(1)}, named, "Record changed"},
	} {
		if got := records.ChangeText(c.c, c.n); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// namedTakes is, per kind, which takes the text names: the other kinds never print a take.
var namedTakes = map[string]string{"slot_added": "to", "slot_removed": "from", "slot_pinned": "to", "slot_unpinned": "to",
	"slot_filled": "to", "slot_emptied": "from", "slot_upgraded": "from,to"}

// TestChangeText_NoTitlesInRows: titles reach the text only through the names lookup. The stored fields (evidence and
// detail) of every row hold ids and numbers, a lookup that answers nothing leaves no title in the text, and a kind
// names exactly the takes of namedTakes.
func TestChangeText_NoTitlesInRows(t *testing.T) {
	t.Parallel()
	for _, r := range textCases() {
		stored, err := json.Marshal(r.Change)
		if err != nil {
			t.Fatal(err)
		}
		for id, title := range titles {
			if strings.Contains(string(stored), title) {
				t.Errorf("%s: the stored row contains the title of %s", r.Name, id)
			}
		}
		silent := records.ChangeText(r.Change, func(string) string { return "" })
		for _, title := range titles {
			if strings.Contains(silent, title) {
				t.Errorf("%s: a title appeared without the lookup: %q", r.Name, silent)
			}
		}
		canary := records.ChangeText(r.Change, func(id string) string { return "«" + id + "»" })
		for side, id := range map[string]*string{"from": r.Change.FromTakeID, "to": r.Change.ToTakeID} {
			if id == nil {
				continue
			}
			want := strings.Contains(namedTakes[r.Change.Kind], side)
			if got := strings.Contains(canary, "«"+*id+"»"); got != want {
				t.Errorf("%s: %s take named = %v, want %v: %q", r.Name, side, got, want, canary)
			}
		}
	}
}
