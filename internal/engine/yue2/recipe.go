package yue2

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/engine"
)

var _ engine.RecipeCodec = Engine{}

// RecipeOf converts a parse result into the engine-neutral recipe, byte for byte.
func RecipeOf(p Parsed) engine.Recipe {
	r := engine.Recipe{ID: p.Request.ID, Style: p.Request.Style, Lyrics: p.Request.Lyrics, Cot: p.Request.Cot,
		Seed: p.Request.Seed, Canonical: p.Canonical, Hash: p.Hash, CoreHash: p.CoreHash}
	for _, w := range p.Warnings {
		r.Warnings = append(r.Warnings, engine.Issue{Code: w, Severity: sevWarning})
	}
	return r
}

// ParseRecipe implements engine.RecipeCodec with ParseRequest (no abc_path resolver, no title).
func (Engine) ParseRecipe(raw json.RawMessage) (engine.Recipe, []engine.Issue) {
	p, err := ParseRequest(raw, ParseOptions{})
	if err != nil {
		return engine.Recipe{}, []engine.Issue{parseErrorIssue(err)}
	}
	return RecipeOf(p), nil
}

func parseErrorIssue(err error) engine.Issue {
	var pe *ParseError
	if errors.As(err, &pe) {
		return engine.Issue{Code: pe.Code, Severity: sevError, Message: pe.Detail, Fatal: true}
	}
	return engine.Issue{Code: "parse_failed", Severity: sevError, Message: err.Error(), Fatal: true}
}

// WithSeedAndID implements engine.RecipeCodec.
func (e Engine) WithSeedAndID(base engine.Recipe, seed int64, id string) (engine.Recipe, error) {
	return e.derive(base, seed, id, nil)
}

// ApplyMode implements engine.RecipeCodec. patch is nil or a *Patch (lineage.Patch is an alias of it); the
// operator rules are the caller's (lineage validates operators before calling).
func (e Engine) ApplyMode(base engine.Recipe, p engine.ModeParams, patch any) (engine.Recipe, error) {
	switch v := patch.(type) {
	case nil:
		return e.derive(base, p.Seed, p.ID, nil)
	case *Patch:
		return e.derive(base, p.Seed, p.ID, v)
	default:
		return engine.Recipe{}, fmt.Errorf("yue2: patch of type %T is not a *yue2.Patch", patch)
	}
}

func (Engine) derive(base engine.Recipe, seed int64, id string, patch *Patch) (engine.Recipe, error) {
	parsed, err := ParseRequest(base.Canonical, ParseOptions{})
	if err != nil {
		return engine.Recipe{}, err
	}
	out, err := ApplyPatch(parsed.Request, seed, id, patch)
	if err != nil {
		return engine.Recipe{}, err
	}
	return RecipeOf(out), nil
}

var _ engine.IngestPolicy = Engine{}

// ReportsTruncation implements engine.IngestPolicy: YuE2 workers report truncated{abc,semantic}.
func (Engine) ReportsTruncation() bool { return true }

// LearnsRTF implements engine.IngestPolicy: rtf_stats is yue2's per-GPU-class factor.
func (Engine) LearnsRTF() bool { return true }
