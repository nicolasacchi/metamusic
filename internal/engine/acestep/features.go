package acestep

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

//go:embed features.verified.json
var featuresJSON []byte

// FeaturesSchema is the schema string of features.verified.json.
const FeaturesSchema = "mm.engine-features/2"

// Structural limits of the verified file (C148.7, D-E2-14).
const (
	// DefaultMaxS is the per-pool duration cap of a pool with no verified entry (24 GB class).
	DefaultMaxS = 480
	// AbsoluteMaxS is the structural ceiling of any render, in seconds.
	AbsoluteMaxS = 600
	// MinS is the shortest text2music render, in seconds.
	MinS = 10
	// SegmentMinS and SegmentMaxS are ACE-Step's documented repaint and extend segment range.
	SegmentMinS, SegmentMaxS = 3, 90
	// SampleRate of every render.
	SampleRate = 48000
)

// Models delivery modes (D-E2-5).
const (
	ModelsBundleOnly       = "bundle_only"
	ModelsBundleBaseBaked  = "bundle_cached_base_baked"
	ModelsTwoCached        = "two_cached"
	defaultPoolA, defaultB = "AMPERE_24", "ADA_24"
)

// DefaultPools are the pools an endpoint is provisioned on when MM_ACESTEP_POOLS is not set.
func DefaultPools() []string { return []string{defaultPoolA, defaultB} }

// Bounds is an inclusive interval in whole seconds.
type Bounds struct {
	MinS int `json:"min_s"`
	MaxS int `json:"max_s"`
}

// PoolFeatures is the verified state of one GPU pool.
type PoolFeatures struct {
	MaxS int `json:"max_s"`
	// CanaryCommit names the passing t2m-long canary; required for a MaxS above DefaultMaxS.
	CanaryCommit string `json:"canary_commit,omitempty"`
}

// Features is features.verified.json (D-E2-7): what the committed canary evidence lets the engine offer.
// The zero value is valid and means text2music only.
type Features struct {
	Schema                string                  `json:"schema"`
	Engine                string                  `json:"engine"`
	VerifiedAt            string                  `json:"verified_at"`
	CanaryCommit          string                  `json:"canary_commit"`
	Tasks                 []string                `json:"tasks"`
	Reference             bool                    `json:"reference"`
	ReferenceReproducible bool                    `json:"reference_reproducible"`
	SeedReproducible      bool                    `json:"seed_reproducible"`
	LRC                   bool                    `json:"lrc"`
	LRCFixedS             int                     `json:"lrc_fixed_s"`
	ModelsMode            string                  `json:"models_mode"`
	MaxSDefault           int                     `json:"max_s_default"`
	Pools                 map[string]PoolFeatures `json:"pools"`
	Repaint               Bounds                  `json:"repaint"`
	Extend                Bounds                  `json:"extend"`
	ReferenceMaxS         int                     `json:"reference_max_s"`
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var poolRe = regexp.MustCompile(`^[A-Z0-9_]{2,40}$`)

// ParseFeatures reads and checks a verified-features file: unknown keys, bad enums and out-of-range bounds are
// errors, and a pool above DefaultMaxS needs the canary that proved it.
func ParseFeatures(raw []byte) (Features, error) {
	var f Features
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Features{}, fmt.Errorf("acestep features: %w", err)
	}
	if dec.More() {
		return Features{}, errors.New("acestep features: trailing data")
	}
	return f, f.check()
}

func (f Features) check() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf("acestep features: "+format, a...)) }
	if f.Schema != FeaturesSchema {
		bad("schema %q, want %q", f.Schema, FeaturesSchema)
	}
	if f.Engine != Name {
		bad("engine %q, want %q", f.Engine, Name)
	}
	if !dateRe.MatchString(f.VerifiedAt) {
		bad("verified_at %q is not a date", f.VerifiedAt)
	}
	for _, t := range f.Tasks {
		if !slices.Contains(allTasks, t) {
			bad("unknown task %q", t)
		}
	}
	if !slices.Contains(f.Tasks, TaskText2Music) && len(f.Tasks) > 0 {
		bad("tasks must contain text2music")
	}
	if len(f.Tasks) > 1 && f.CanaryCommit == "" {
		bad("tasks beyond text2music need a canary_commit")
	}
	if !slices.Contains([]string{ModelsBundleOnly, ModelsBundleBaseBaked, ModelsTwoCached}, f.ModelsMode) {
		bad("models_mode %q", f.ModelsMode)
	}
	if f.MaxSDefault < MinS || f.MaxSDefault > DefaultMaxS {
		bad("max_s_default %d outside %d..%d", f.MaxSDefault, MinS, DefaultMaxS)
	}
	for name, p := range f.Pools {
		if !poolRe.MatchString(name) {
			bad("pool id %q", name)
		}
		switch {
		case p.MaxS < MinS || p.MaxS > AbsoluteMaxS:
			bad("pool %s max_s %d outside %d..%d", name, p.MaxS, MinS, AbsoluteMaxS)
		case p.MaxS > DefaultMaxS && p.CanaryCommit == "":
			bad("pool %s max_s %d above %d needs a canary_commit", name, p.MaxS, DefaultMaxS)
		}
	}
	for n, b := range map[string]Bounds{"repaint": f.Repaint, "extend": f.Extend} {
		if b.MinS < SegmentMinS || b.MaxS > SegmentMaxS || b.MinS > b.MaxS {
			bad("%s bounds %d..%d outside %d..%d", n, b.MinS, b.MaxS, SegmentMinS, SegmentMaxS)
		}
	}
	if f.ReferenceMaxS < 2 || f.ReferenceMaxS > 30 {
		bad("reference_max_s %d outside 2..30", f.ReferenceMaxS)
	}
	if f.LRCFixedS < 0 || f.LRCFixedS > 600 {
		bad("lrc_fixed_s %d outside 0..600", f.LRCFixedS)
	}
	return errors.Join(errs...)
}

// EmbeddedFeatures is the committed features.verified.json. It panics if the embedded file is invalid (a build
// defect, pinned by TestFeatures_FileSafe).
func EmbeddedFeatures() Features {
	f, err := ParseFeatures(featuresJSON)
	if err != nil {
		panic(err)
	}
	return f
}

func (f Features) has(task string) bool { return slices.Contains(f.Tasks, task) }

// baseModelsLoaded reports whether the base DiT is delivered to the endpoint at all.
func (f Features) baseModelsLoaded() bool {
	return f.ModelsMode == ModelsBundleBaseBaked || f.ModelsMode == ModelsTwoCached
}

// MaxSFor is the verified duration cap of the pools: the minimum over pools of pools[p].max_s, a pool with no
// entry counting max_s_default (DefaultMaxS when that is unset). No pools means the default pools.
func (f Features) MaxSFor(pools []string) int {
	if len(pools) == 0 {
		pools = DefaultPools()
	}
	def := f.MaxSDefault
	if def == 0 {
		def = DefaultMaxS
	}
	out := AbsoluteMaxS
	for _, p := range pools {
		v := def
		if e, ok := f.Pools[p]; ok {
			v = e.MaxS
		}
		out = min(out, v)
	}
	return out
}
