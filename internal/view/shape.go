package view

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Rules say which response keys a viewer must not receive (C308.3). Each list is a set of paths, applied only when the
// viewer lacks the matching right; a viewer with every right gets the body untouched, so the Owner, agents and the
// Owner's devices see byte-identical responses.
//
// A path is dotted JSON keys. Arrays are traversed transparently ("items.track.seed" reaches every item). A leading "**"
// matches the rest of the path at any depth ("**.seed"). The last key is deleted from every object the path reaches.
type Rules struct {
	Cost    []string // removed when !SeeCost (Editor and Listener)
	Muse    []string // removed when !SeeMuse (Listener)
	Private []string // removed when !SeePrivate (Listener)
	Editor  []string // removed for an Editor only
	Empty   []string // for !SeePrivate: the value at the path becomes an empty array
	// MoneyKeys removes every key that names money (MoneyKey) at any depth when !SeeCost.
	MoneyKeys bool
}

// Needed reports whether the rules change anything for v.
func (r Rules) Needed(v Viewer) bool {
	return (!v.SeeCost && (len(r.Cost) > 0 || r.MoneyKeys)) || (!v.SeeMuse && len(r.Muse) > 0) ||
		(!v.SeePrivate && (len(r.Private) > 0 || len(r.Empty) > 0)) || (v.Role == auth.RoleEditor && len(r.Editor) > 0)
}

// MoneyKey reports whether a JSON key names a money amount: `*_micro_usd`, `*_usd`, `display_*`, the cost-display keys
// and `cost_basis` (D-U1-12). Amounts are never floats; this only decides what a restricted viewer does not get.
func MoneyKey(k string) bool {
	return strings.HasSuffix(k, "_micro_usd") || strings.HasSuffix(k, "_usd") || strings.HasPrefix(k, "display_") ||
		strings.HasPrefix(k, "cost_display") || k == "cost_basis" || k == "cost_micro_usd"
}

// Shape returns body with the keys v may not see removed. For a viewer the rules do not touch, body itself is returned
// (not a copy). Otherwise the body is round-tripped through JSON (numbers kept exact) and the result is a generic
// value that marshals to the same bytes as the typed body, minus the removed keys.
func (v Viewer) Shape(body any, r Rules) (any, error) {
	if !r.Needed(v) {
		return body, nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var node any
	if err := dec.Decode(&node); err != nil {
		return nil, err
	}
	del := func(paths []string) {
		for _, p := range paths {
			deletePath(node, strings.Split(p, "."))
		}
	}
	if !v.SeeCost {
		del(r.Cost)
		if r.MoneyKeys {
			deleteWhere(node, MoneyKey)
		}
	}
	if !v.SeeMuse {
		del(r.Muse)
	}
	if !v.SeePrivate {
		del(r.Private)
		for _, p := range r.Empty {
			emptyPath(node, strings.Split(p, "."))
		}
	}
	if v.Role == auth.RoleEditor {
		del(r.Editor)
	}
	return node, nil
}

func deletePath(n any, segs []string) {
	if len(segs) == 0 {
		return
	}
	switch x := n.(type) {
	case []any:
		for _, e := range x {
			deletePath(e, segs)
		}
	case map[string]any:
		if segs[0] == "**" {
			deletePath(x, segs[1:])
			for _, c := range x {
				deletePath(c, segs)
			}
			return
		}
		if len(segs) == 1 {
			delete(x, segs[0])
			return
		}
		if c, ok := x[segs[0]]; ok {
			deletePath(c, segs[1:])
		}
	}
}

func emptyPath(n any, segs []string) {
	switch x := n.(type) {
	case []any:
		for _, e := range x {
			emptyPath(e, segs)
		}
	case map[string]any:
		if len(segs) == 1 {
			if _, ok := x[segs[0]]; ok {
				x[segs[0]] = []any{}
			}
			return
		}
		if c, ok := x[segs[0]]; ok {
			emptyPath(c, segs[1:])
		}
	}
}

func deleteWhere(n any, pred func(string) bool) {
	switch x := n.(type) {
	case []any:
		for _, e := range x {
			deleteWhere(e, pred)
		}
	case map[string]any:
		for k, c := range x {
			if pred(k) {
				delete(x, k)
				continue
			}
			deleteWhere(c, pred)
		}
	}
}
