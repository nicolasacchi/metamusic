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
	if len(shapeRules) != len(u1ShapeRules) || len(wireNames) != len(baseWireNames) || len(wireNameTaken) != len(baseWireNames) {
		t.Fatalf("registries hold %d/%d/%d entries, base tables %d/%d", len(shapeRules), len(wireNames), len(wireNameTaken), len(u1ShapeRules), len(baseWireNames))
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
	addItemModes("g1_selftest_mode")
	defer func() { itemModes = itemModes[:len(itemModes)-1] }()
	a := New(http.NewServeMux(), Deps{Version: "x"})
	s := a.OpenAPI().Components.Schemas.Map()["ItemIn"]
	if s == nil || s.Properties["mode"] == nil {
		t.Fatal("ItemIn.mode schema missing")
	}
	var got []string
	for _, e := range s.Properties["mode"].Enum {
		got = append(got, e.(string))
	}
	want := "new_seed,same_seed_edit,lyric_edit,style_edit,from_draft,compose_new,from_template,song_pr,port,extend,repaint,reference,complete,stems,cover,g1_selftest_mode"
	if strings.Join(got, ",") != want {
		t.Errorf("mode enum = %v", got)
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
