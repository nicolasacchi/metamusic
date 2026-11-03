package acestep

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/canonjson"
	"github.com/nicolasacchi/metamusic/internal/engine"
)

var _ engine.RecipeCodec = Engine{}

// OptInt and OptString are tri-state patch values: absent, JSON null (remove) or a value.
type OptInt struct {
	Set, Null bool
	Value     int
}

// OptString is the string twin of OptInt.
type OptString struct {
	Set, Null bool
	Value     string
}

// Patch edits a base recipe (the engine's own typed patch for ApplyMode). nil pointers and unset values keep
// the base value. The edit fields (Src, Ref, ranges, ExtendDS, Tracks) carry the mode's inputs.
type Patch struct {
	Style, Lyrics              *string // Style is the caption
	Thinking                   *bool   // the cot switch
	BPM                        OptInt
	KeyScale, TimeSignature    OptString
	DurationDS                 *int
	Src                        *SrcRef
	Ref                        *RefRef
	RepaintFromDS, RepaintToDS *int
	ExtendDS                   *int
	Tracks                     []string
}

// RecipeOf converts a parse result into the engine-neutral recipe (C148.5): hash = sha256 of the canonical
// bytes, core hash = sha256 of canonical {cot, id, lyrics, seed, style, task}.
func RecipeOf(r Request, canonical []byte, warnings []engine.Issue) engine.Recipe {
	cot := "off"
	if r.Thinking {
		cot = "think"
	}
	core, _ := json.Marshal(map[string]any{"cot": cot, "id": r.ID, "lyrics": r.Lyrics, "seed": r.Seed, "style": r.Caption, "task": r.Task})
	cc, _ := canonjson.Canonical(core)
	return engine.Recipe{ID: r.ID, Style: r.Caption, Lyrics: r.Lyrics, Cot: cot, Seed: r.Seed,
		Canonical: canonical, Hash: sha(canonical), CoreHash: sha(cc), Warnings: warnings}
}

func warnings(is []engine.Issue) (w []engine.Issue) {
	for _, i := range is {
		if i.Severity == "warning" {
			w = append(w, i)
		}
	}
	return w
}

// ParseRecipe implements engine.RecipeCodec.
func (Engine) ParseRecipe(raw json.RawMessage) (engine.Recipe, []engine.Issue) {
	r, c, is := Parse(raw)
	if HasErrors(is) {
		return engine.Recipe{}, is
	}
	return RecipeOf(r, c, warnings(is)), nil
}

// WithSeedAndID implements engine.RecipeCodec.
func (e Engine) WithSeedAndID(base engine.Recipe, seed int64, id string) (engine.Recipe, error) {
	return e.derive(base, engine.ModeParams{Seed: seed, ID: id}, nil)
}

// ApplyMode implements engine.RecipeCodec. patch is nil or a *Patch. The mode selects the task of the child
// (extend, repaint, reference, complete, stems) or keeps text2music (port, variants); the lineage rules
// (parents, operators) are the caller's.
func (e Engine) ApplyMode(base engine.Recipe, p engine.ModeParams, patch any) (engine.Recipe, error) {
	switch v := patch.(type) {
	case nil:
		return e.derive(base, p, nil)
	case *Patch:
		return e.derive(base, p, v)
	default:
		return engine.Recipe{}, fmt.Errorf("acestep: patch of type %T is not a *acestep.Patch", patch)
	}
}

func clone(r Request) Request {
	o := r
	for _, p := range []**int{&o.BPM, &o.GuidanceMilli, &o.RepaintFromDS, &o.RepaintToDS, &o.ExtendDS} {
		if *p != nil {
			v := **p
			*p = &v
		}
	}
	if r.Src != nil {
		v := *r.Src
		o.Src = &v
	}
	if r.Ref != nil {
		v := *r.Ref
		o.Ref = &v
	}
	o.Tracks = slices.Clone(r.Tracks)
	return o
}

func (Engine) derive(base engine.Recipe, p engine.ModeParams, patch *Patch) (engine.Recipe, error) {
	br, _, is := Parse(base.Canonical)
	if HasErrors(is) {
		return engine.Recipe{}, errors.New("acestep: base recipe does not parse: " + is[0].Code)
	}
	r := clone(br)
	var warns []engine.Issue
	if patch != nil {
		applyFields(&r, patch)
	}
	if err := applyMode(&r, p.Mode, patch, br); err != nil {
		return engine.Recipe{}, err
	}
	r.Seed = p.Seed
	if p.ID != "" {
		r.ID = p.ID
	}
	if r.ParamsVersion != CurrentParamsVersion() {
		warns = append(warns, warn("params_version_changed", "engine_params_version", "recipe re-pinned to the current engine parameters"))
		r.ParamsVersion = CurrentParamsVersion()
	}
	// Defaults are re-derived for the child's class.
	r.Steps, r.GuidanceMilli = 0, nil
	raw, err := json.Marshal(r)
	if err != nil {
		return engine.Recipe{}, err
	}
	out, c, is := Parse(raw)
	if HasErrors(is) {
		return engine.Recipe{}, errors.New("acestep: derived recipe invalid: " + firstError(is))
	}
	return RecipeOf(out, c, append(warns, warnings(is)...)), nil
}

func firstError(is []engine.Issue) string {
	for _, i := range is {
		if i.Severity == "error" {
			return i.Code + " (" + i.Field + ")"
		}
	}
	return ""
}

func applyFields(r *Request, p *Patch) {
	if p.Style != nil {
		r.Caption = *p.Style
	}
	if p.Lyrics != nil {
		r.Lyrics = *p.Lyrics
		r.Instrumental = *p.Lyrics == ""
	}
	if p.Thinking != nil {
		r.Thinking = *p.Thinking
	}
	switch {
	case p.BPM.Set && p.BPM.Null:
		r.BPM = nil
	case p.BPM.Set:
		v := p.BPM.Value
		r.BPM = &v
	}
	switch {
	case p.KeyScale.Set && p.KeyScale.Null:
		r.KeyScale = ""
	case p.KeyScale.Set:
		r.KeyScale = p.KeyScale.Value
	}
	switch {
	case p.TimeSignature.Set && p.TimeSignature.Null:
		r.TimeSignature = ""
	case p.TimeSignature.Set:
		r.TimeSignature = p.TimeSignature.Value
	}
	if p.DurationDS != nil {
		r.DurationDS = *p.DurationDS
	}
}

// applyMode sets the task and the task's inputs, clearing the fields the new task does not have.
func applyMode(r *Request, mode string, p *Patch, base Request) error {
	if p == nil {
		p = &Patch{}
	}
	clearEdit := func() {
		r.Src, r.RepaintFromDS, r.RepaintToDS, r.ExtendDS, r.Tracks = nil, nil, nil, nil, nil
	}
	switch mode {
	case "", ModeNewSeed, ModeSameSeedEdit, ModeLyricEdit, ModeStyleEdit, ModePort, ModeComposeNew:
		if base.Task != TaskText2Music {
			return fmt.Errorf("acestep: mode %q needs a text2music base, got %s", mode, base.Task)
		}
	case ModeReference:
		if p.Ref == nil {
			return errors.New("acestep: reference mode needs a reference")
		}
		if r.DurationDS == 0 && base.Src != nil {
			r.DurationDS = base.Src.DurationDS
		}
		clearEdit()
		r.Task, r.Ref = TaskText2Music, p.Ref
	case ModeRepaint:
		clearEdit()
		r.Task, r.Thinking, r.DurationDS, r.LRC = TaskRepaint, false, 0, false
		r.Src, r.RepaintFromDS, r.RepaintToDS = p.Src, p.RepaintFromDS, p.RepaintToDS
	case ModeExtend:
		clearEdit()
		r.Task, r.Thinking, r.DurationDS, r.Ref, r.LRC = TaskExtend, false, 0, nil, false
		r.Src, r.ExtendDS = p.Src, p.ExtendDS
	case ModeComplete, ModeStems:
		clearEdit()
		r.Task, r.Thinking, r.DurationDS, r.Ref, r.LRC = TaskComplete, false, 0, nil, false
		if mode == ModeStems {
			r.Task = TaskExtract
		}
		r.Src, r.Tracks = p.Src, slices.Clone(p.Tracks)
	default:
		return fmt.Errorf("acestep: mode %q is not an engine mode", mode)
	}
	if mode != ModeReference && r.Task != TaskText2Music && p.Src == nil {
		return fmt.Errorf("acestep: mode %q needs a source", mode)
	}
	if r.Task == TaskText2Music {
		r.Src, r.RepaintFromDS, r.RepaintToDS, r.ExtendDS, r.Tracks = nil, nil, nil, nil, nil
	}
	return nil
}
