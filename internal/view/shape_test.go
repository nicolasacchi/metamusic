package view_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/view"
)

type sample struct {
	Items []map[string]any `json:"items"`
	Song  map[string]any   `json:"song"`
	Big   int64            `json:"big"`
}

func body() sample {
	return sample{
		Items: []map[string]any{
			{"id": "a", "seed": 7, "cost_display_usd": "0.10", "muse_id": "m1", "track": map[string]any{"seed": 1, "title": "t"}},
			{"id": "b", "seed": 8, "cost_display_usd": nil, "muse_id": nil},
		},
		Song: map[string]any{"muse_id": "m1", "draft": map[string]any{"id": "d"}, "log": []any{"x"}, "n_micro_usd": 5},
		Big:  9007199254740993, // 2^53+1: must survive the round trip exactly
	}
}

func viewer(r auth.Role) view.Viewer {
	return view.For(auth.Principal{Kind: auth.KindHuman, ID: "x", User: "x", Role: r})
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestShape(t *testing.T) {
	t.Parallel()
	rules := view.Rules{
		Cost:      []string{"song.cost"},
		Muse:      []string{"items.muse_id", "song.muse_id"},
		Private:   []string{"**.seed", "song.draft"},
		Editor:    []string{"song.log"},
		Empty:     []string{"song.log"},
		MoneyKeys: true,
	}
	b := body()
	orig := marshal(t, b)
	// the Owner side gets the typed body itself, not a copy
	for _, r := range []auth.Role{auth.RoleOwner, ""} {
		got, err := viewer(r).Shape(b, rules)
		if err != nil || marshal(t, got) != orig {
			t.Errorf("role %q: %v %s", r, err, marshal(t, got))
		}
	}
	// an Editor loses the money keys and the Editor list only
	ed, err := viewer(auth.RoleEditor).Shape(b, rules)
	if err != nil {
		t.Fatal(err)
	}
	if s := marshal(t, ed); strings.Contains(s, "cost_display_usd") || strings.Contains(s, "n_micro_usd") || strings.Contains(s, `"log"`) || !strings.Contains(s, `"seed":7`) || !strings.Contains(s, "muse_id") {
		t.Errorf("editor: %s", s)
	}
	// a Listener loses money, Muse, private and the emptied list; the big integer stays exact
	ls, err := viewer(auth.RoleListener).Shape(b, rules)
	if err != nil {
		t.Fatal(err)
	}
	s := marshal(t, ls)
	for _, bad := range []string{"seed", "cost_display_usd", "muse_id", "draft", "n_micro_usd"} {
		if strings.Contains(s, bad) {
			t.Errorf("listener still has %q: %s", bad, s)
		}
	}
	if !strings.Contains(s, `"big":9007199254740993`) || !strings.Contains(s, `"log":[]`) || !strings.Contains(s, `"title":"t"`) {
		t.Errorf("listener body: %s", s)
	}
	// the typed value is untouched
	if marshal(t, b) != orig {
		t.Errorf("Shape mutated its input")
	}
}

func TestShapeEdges(t *testing.T) {
	t.Parallel()
	l := viewer(auth.RoleListener)
	if _, err := l.Shape(make(chan int), view.Rules{Private: []string{"a"}}); err == nil {
		t.Errorf("an unmarshalable body is not an error")
	}
	// a path that misses, on a scalar root, an array root, and a nested miss
	for _, in := range []any{1, []any{map[string]any{"a": 1}}, map[string]any{"x": map[string]any{"y": 1}}} {
		got, err := l.Shape(in, view.Rules{Private: []string{"a.b.c", "x.z.y", "**.nope"}, Empty: []string{"q.r", "x.nope"}})
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(marshal(t, in), marshal(t, got)); diff != "" {
			t.Errorf("a missing path changed the body: %s", diff)
		}
	}
	// nested Empty and an array root
	got, _ := l.Shape([]any{map[string]any{"c": map[string]any{"changes": []any{1}}}}, view.Rules{Empty: []string{"c.changes"}})
	if marshal(t, got) != `[{"c":{"changes":[]}}]` {
		t.Errorf("nested empty: %s", marshal(t, got))
	}
	// Needed
	if (view.Rules{}).Needed(l) || !(view.Rules{MoneyKeys: true}).Needed(l) || (view.Rules{Muse: []string{"a"}}).Needed(viewer(auth.RoleEditor)) ||
		!(view.Rules{Editor: []string{"a"}}).Needed(viewer(auth.RoleEditor)) || (view.Rules{Editor: []string{"a"}}).Needed(l) {
		t.Errorf("Needed")
	}
}

func TestMoneyKey(t *testing.T) {
	t.Parallel()
	for k, want := range map[string]bool{
		"cost_micro_usd": true, "price_usd": true, "display_usd": true, "display_micro_usd": true, "cost_display_usd": true,
		"cost_basis": true, "cost": false, "cost_display": true, "title": false, "usd_rate": false, "seed": false,
	} {
		if got := view.MoneyKey(k); got != want {
			t.Errorf("MoneyKey(%q) = %v", k, got)
		}
	}
}

func TestBannedFilterKey(t *testing.T) {
	t.Parallel()
	for k, want := range map[string]string{"no_muse": "muse_id", "muse_id": "muse_id", "angle": "angle", "unheard": "unheard", "state": ""} {
		got, ok := view.BannedFilterKey(k)
		if got != want || ok != (want != "") {
			t.Errorf("BannedFilterKey(%q) = %q, %v", k, got, ok)
		}
	}
}
