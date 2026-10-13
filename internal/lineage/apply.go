package lineage

import (
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// Parsed is a validated engine request (yue2): the canonical bytes, its hash and the typed request.
// generation holds values of this type without importing the engine implementation.
type Parsed = yue2.Parsed

// Request is the typed engine request.
type Request = yue2.Request

// Issue is one lint finding.
type Issue = yue2.Issue

// ParseError is a request that fails the protocol rules; Code is stable.
type ParseError = yue2.ParseError

// MaxSeed is the largest seed (2^53 - 1, D-M1-7).
const MaxSeed = yue2.MaxSeed

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

// Params are the recorded parameters of a mutation (jobs.mutation_json.params, mutations.params_json).
// Seed and ID are the child's own values, not stored in params_json: a replay supplies them from the
// child's recipe.
type Params struct {
	Mode       string  `json:"mode"`
	SeedPolicy string  `json:"seed_policy"`
	NSeeds     int     `json:"n_seeds"`
	Via        *string `json:"via"`

	Seed int64  `json:"-"`
	ID   string `json:"-"`
}

// ParseRequest validates one engine request. allowTitle makes "title" app metadata instead of a
// dropped key. No abc_path resolver: an abc_path in a composed recipe is an error.
func ParseRequest(raw []byte, allowTitle bool) (Parsed, error) {
	return yue2.ParseRequest(raw, yue2.ParseOptions{AllowTitle: allowTitle})
}

// ErrBadOperator is returned by Apply for an operator that does not edit a recipe.
var ErrBadOperator = errors.New("lineage: unknown operator")

// Apply derives a child request: it starts from base, sets or removes the patched keys, sets the seed and
// id of params, re-runs the protocol checks and returns the canonical request. It is pure and is what a
// replay uses (TestMutation_ReplayReproducesRender).
func Apply(base yue2.Request, op string, params Params, patch *Patch) (yue2.Request, error) {
	p, err := ApplyParsed(base, op, params, patch)
	if err != nil {
		return yue2.Request{}, err
	}
	return p.Request, nil
}

// ApplyParsed is Apply returning the full parse result (canonical bytes and hashes).
func ApplyParsed(base yue2.Request, op string, params Params, patch *Patch) (yue2.Parsed, error) {
	if !ValidOperator(op) || op == OpInferredSibling {
		return yue2.Parsed{}, fmt.Errorf("%w: %q", ErrBadOperator, op)
	}
	r := base
	// copy the pointers so the base is never aliased
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
	r.Seed = params.Seed
	if params.ID != "" {
		r.ID = params.ID
	}
	return yue2.ParseRequest(yue2.CanonicalJSON(r), yue2.ParseOptions{})
}

// Lint runs the protocol and songwriting checks over one canonical request and splits the findings.
func Lint(canonical []byte) (errs, warns []Issue) {
	rep := yue2.Lint([]yue2.LintItem{{Request: canonical}}, false, yue2.LintContext{})
	if len(rep.Items) == 0 {
		return nil, nil
	}
	return rep.Items[0].Errors, rep.Items[0].Warnings
}

// RequestOf parses a stored canonical request (recipes.request_json) into its typed form.
func RequestOf(canonical []byte) (yue2.Request, error) {
	p, err := yue2.ParseRequest(canonical, yue2.ParseOptions{})
	if err != nil {
		return yue2.Request{}, err
	}
	return p.Request, nil
}
