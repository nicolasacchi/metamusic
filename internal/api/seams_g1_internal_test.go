package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/view"
)

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", name)
		}
	}()
	f()
}

// The registries panic on a duplicate, and nothing is half-registered by the refused call's earlier keys being re-checked.
func TestSeams_DuplicateRegistrationPanics(t *testing.T) {
	t.Parallel()
	mustPanic(t, "shape rule", func() { addShapeRules(map[string]view.Rules{"get_song": {}}) })
	mustPanic(t, "wire type", func() { addWireNames(map[string]string{"internal/backup.Run": "SomethingElse"}) })
	mustPanic(t, "wire name", func() { addWireNames(map[string]string{"internal/nosuch.Type": "BackupRun"}) })
	mustPanic(t, "item mode", func() { addItemModes("cover") })
	mustPanic(t, "empty item mode", func() { addItemModes("") })
}

// The tables hold exactly what the in-place maps held (the existing contents moved behind the helpers).
func TestSeams_BaseTablesMoved(t *testing.T) {
	t.Parallel()
	// containment, not equality: track lanes register more entries from their own files.
	for typ, name := range baseWireNames {
		if wireNames[typ] != name {
			t.Errorf("wire name for %s = %q, want %q", typ, wireNames[typ], name)
		}
		if wireNameTaken[name] != typ {
			t.Errorf("wire name %s not taken by %s", name, typ)
		}
	}
	for id, r := range u1ShapeRules {
		if !reflect.DeepEqual(shapeRules[id], r) {
			t.Errorf("shape rules for %s differ", id)
		}
	}
	if got := schemaNamer(reflect.TypeFor[Pot](), ""); got != "Pot" {
		t.Errorf("schemaNamer(Pot) = %q", got)
	}
}

// ItemIn.Mode keeps the enum of its struct tag, in the same order, in the document and the validator; a registered mode is appended.
func TestSeams_ItemModeEnum(t *testing.T) {
	// not parallel: it registers a mode in the package table
	n := len(itemModes)
	addItemModes("g1_selftest_mode")
	defer func() { itemModes = itemModes[:n] }()
	a := New(http.NewServeMux(), Deps{Version: "x"})
	s := a.OpenAPI().Components.Schemas.Map()["ItemIn"]
	if s == nil || s.Properties["mode"] == nil {
		t.Fatal("ItemIn.mode schema missing")
	}
	var got []string
	for _, e := range s.Properties["mode"].Enum {
		got = append(got, e.(string))
	}
	base := "new_seed,same_seed_edit,lyric_edit,style_edit,from_draft,compose_new,from_template,song_pr,port,extend,repaint,reference,complete,stems,cover"
	// the tag modes come first in order; modes registered by track lanes follow, and the test mode is last.
	if !strings.HasPrefix(strings.Join(got, ",")+",", base+",") {
		t.Errorf("mode enum %v does not start with the base modes", got)
	}
	if len(got) == 0 || got[len(got)-1] != "g1_selftest_mode" {
		t.Errorf("mode enum %v does not end with the registered test mode", got)
	}
}

func TestDeps_FillTakesOnlyZeroFields(t *testing.T) {
	t.Parallel()
	got := Deps{Version: "own"}.Fill(Deps{Version: "base", Commit: "c1"})
	if got.Version != "own" || got.Commit != "c1" {
		t.Errorf("Fill = %+v", got)
	}
	// every field is reachable (an unexported field would be skipped silently)
	for i := range reflect.TypeFor[Deps]().NumField() {
		if f := reflect.TypeFor[Deps]().Field(i); !f.IsExported() {
			t.Errorf("Deps.%s is unexported; Fill cannot copy it", f.Name)
		}
	}
}
