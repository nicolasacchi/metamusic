package yue2

// OptString is a tri-state patch string: absent, JSON null (remove) or a value (set).
type OptString struct {
	Set, Null bool
	Value     string
}

// OptNumber is the numeric twin of OptString.
type OptNumber struct {
	Set, Null bool
	Value     float64
}

// Patch edits a base recipe (C71.1); nil pointers and unset values keep the base value.
type Patch struct {
	Style, Lyrics, Cot *string
	ABC                OptString
	CFGScale           OptNumber
}

// IsEmpty reports a patch that changes nothing.
func (p *Patch) IsEmpty() bool {
	return p == nil || (p.Style == nil && p.Lyrics == nil && p.Cot == nil && !p.ABC.Set && !p.CFGScale.Set)
}

// ApplyPatch derives a child request from base: it sets or removes the patched keys, sets the seed and (when
// id is not empty) the id, re-runs the protocol checks and returns the parse result. It is pure; the base is
// never aliased.
func ApplyPatch(base Request, seed int64, id string, patch *Patch) (Parsed, error) {
	r := base
	if base.ABC != nil {
		v := *base.ABC
		r.ABC = &v
	}
	if base.CFGScale != nil {
		v := *base.CFGScale
		r.CFGScale = &v
	}
	if patch != nil {
		if patch.Style != nil {
			r.Style = *patch.Style
		}
		if patch.Lyrics != nil {
			r.Lyrics = *patch.Lyrics
		}
		if patch.Cot != nil {
			r.Cot = *patch.Cot
		}
		switch {
		case patch.ABC.Set && patch.ABC.Null:
			r.ABC = nil
		case patch.ABC.Set:
			v := patch.ABC.Value
			r.ABC = &v
		}
		switch {
		case patch.CFGScale.Set && patch.CFGScale.Null:
			r.CFGScale = nil
		case patch.CFGScale.Set:
			v := patch.CFGScale.Value
			r.CFGScale = &v
		}
	}
	r.Seed = seed
	if id != "" {
		r.ID = id
	}
	return ParseRequest(CanonicalJSON(r), ParseOptions{})
}
