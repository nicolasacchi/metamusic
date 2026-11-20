package api

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/view"
)

// Per-file registration for the three package-level tables every track used to edit in place (G1 D-G1-12, Revision 2). A track adds
// shape_<track>.go, wire_<track>.go and item_modes_<track>.go, each with an init() that calls the helper below; a duplicate is a
// programming error and panics at start-up, so two lanes cannot silently shadow each other. Registration is init-time only: the
// tables are read without a lock afterwards.

// shapeRules is the per-operation shaping rules, keyed by operation id; filled by addShapeRules.
var shapeRules = map[string]view.Rules{}

// addShapeRules registers per-operation shaping rules (see shape_u1.go). An operation id registered twice panics.
func addShapeRules(rules map[string]view.Rules) {
	for id, r := range rules {
		if _, dup := shapeRules[id]; dup {
			panic(fmt.Sprintf("api: shape rules for %q registered twice", id))
		}
		shapeRules[id] = r
	}
}

// wireNames gives schema components of package-local Go names a name that is unambiguous in the one OpenAPI namespace (Go type ->
// component name); filled by addWireNames.
var (
	wireNames     = map[string]string{}
	wireNameTaken = map[string]string{} // component name -> Go type, to refuse two types with one name
)

// addWireNames registers Go type -> component name pairs (see wire_base.go). The key is "internal/<pkg>.<Type>". A type or a
// component name registered twice panics.
func addWireNames(names map[string]string) {
	for typ, name := range names {
		if _, dup := wireNames[typ]; dup {
			panic(fmt.Sprintf("api: wire name for %q registered twice", typ))
		}
		if other, dup := wireNameTaken[name]; dup {
			panic(fmt.Sprintf("api: wire name %q is used by both %q and %q", name, other, typ))
		}
		wireNames[typ], wireNameTaken[name] = name, typ
	}
}

// Fill returns d with every zero field replaced by the same field of base. Non-zero fields of d win. httpserver.New uses it to take
// the dependencies a track added to Deps from Options.Deps without a line per field in server.go.
func (d Deps) Fill(base Deps) Deps {
	dv, bv := reflect.ValueOf(&d).Elem(), reflect.ValueOf(base)
	for i := range dv.NumField() {
		if f := dv.Field(i); f.CanSet() && f.IsZero() {
			f.Set(bv.Field(i))
		}
	}
	return d
}

// itemModes are the ItemIn.Mode values registered after the ones in the struct tag, in registration order.
var (
	itemModesMu sync.Mutex
	itemModes   []string
)

// addItemModes registers ItemIn.Mode values (see item_modes.go). A mode already in the struct tag or registered twice panics.
func addItemModes(modes ...string) {
	itemModesMu.Lock()
	defer itemModesMu.Unlock()
	for _, m := range modes {
		if m == "" || strings.ContainsAny(m, ", ") {
			panic(fmt.Sprintf("api: invalid item mode %q", m))
		}
		f, _ := reflect.TypeFor[ItemIn]().FieldByName("Mode")
		for _, have := range append(strings.Split(f.Tag.Get("enum"), ","), itemModes...) {
			if have == m {
				panic(fmt.Sprintf("api: item mode %q registered twice", m))
			}
		}
		itemModes = append(itemModes, m)
	}
}
