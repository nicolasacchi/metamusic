package provider

import (
	"fmt"
	"slices"
)

// The mm.worker/2 artifact table (C321.5). One table, mirrored by worker/src/mmworker/contract.py and by the enums of the two /2
// schemas; TestArtifacts_V2SchemaEnum checks the schema half in both directions and TestWriteArtifactsV2 exports the table to
// worker/contract/testdata/artifacts-v2.json for the Python test.
//
//	kind                    upload targets                                         reported once finished
//	render (no ears)        the /1 set                                             the /1 set
//	render with ears        the /1 set, analysis.json, stem-vocals.flac (keep)     + analysis.json once ears.status = ok
//	analyze                 analysis.json, stem-vocals.flac (keep), mm-result.json analysis.json
//	plan (n seeds)          plan-00.tar .. plan-(n-1).tar, mm-result.json          plan-NN.tar of each plan with status ok
const (
	// AnalysisArtifact is the raw Tier B document, contract mm.ears-b/1 (the Ears track owns its schema).
	AnalysisArtifact = "analysis.json"
	// StemVocalsArtifact is the separated vocal stem, uploaded only when ears.keep_vocals asks for it.
	StemVocalsArtifact = "stem-vocals.flac"

	maxAnalysisBytes = 4 * mib
	maxStemBytes     = 200 * mib
	// MaxPlanBundleBytes is the limit of one plan-NN.tar (Revision 2, desk A1: 2 MiB; C321.5 said 160 MiB).
	MaxPlanBundleBytes = 2 * mib
	// MaxPlanSeeds is the largest number of seeds of a plan document (and so of plan-NN.tar artifacts).
	MaxPlanSeeds = 16
	// MaxAttachBytes bounds the decoded size of every attachment of a document together (C321.3).
	MaxAttachBytes = 1 * mib
)

// KindShape is what decides the artifact set of a /2 document: its kind, whether a Tier B stage and its vocal stem are asked
// for, and the number of plan seeds.
type KindShape struct {
	Kind       string // "" is render
	Ears       bool
	KeepVocals bool
	Seeds      int
}

// ShapeOf reads the shape of a take spec.
func ShapeOf(t TakeSpec) KindShape {
	s := KindShape{Kind: t.Kind}
	if s.Kind == "" {
		s.Kind = KindRender
	}
	if t.Ears != nil {
		s.Ears, s.KeepVocals = true, t.Ears.KeepVocals
	}
	if t.Plan != nil {
		s.Seeds = len(t.Plan.Seeds)
	}
	return s
}

// PlanBundleName is the artifact name of plan seed i.
func PlanBundleName(i int) string { return fmt.Sprintf("plan-%02d.tar", i) }

func (s KindShape) kind() string {
	if s.Kind == "" {
		return KindRender
	}
	return s.Kind
}

// ArtifactsV2 is the upload allowlist of a /2 document of the given shape, in upload order with mm-result.json last. Required
// marks the targets the document must carry. A render keeps the /1 yue2 set and its conditional members; an unknown kind has
// none (nil).
func ArtifactsV2(s KindShape) []Artifact {
	result := Artifact{ResultArtifact, 64 * kib, true}
	switch s.kind() {
	case KindRender:
		list := slices.Clone(Artifacts[:len(Artifacts)-1]) // without mm-result.json
		if s.Ears {
			if s.KeepVocals {
				list = append(list, Artifact{StemVocalsArtifact, maxStemBytes, true})
			}
			list = append(list, Artifact{AnalysisArtifact, maxAnalysisBytes, true})
		}
		return append(list, result)
	case KindAnalyze:
		list := []Artifact{{AnalysisArtifact, maxAnalysisBytes, true}}
		if s.KeepVocals {
			list = append(list, Artifact{StemVocalsArtifact, maxStemBytes, true})
		}
		return append(list, result)
	case KindPlan:
		var list []Artifact
		for i := 0; i < min(s.Seeds, MaxPlanSeeds); i++ {
			list = append(list, Artifact{PlanBundleName(i), MaxPlanBundleBytes, true})
		}
		return append(list, result)
	}
	return nil
}

// ArtifactMaxBytesV2 is the limit of an artifact in the allowlist of the shape.
func ArtifactMaxBytesV2(s KindShape, name string) (int64, bool) {
	for _, a := range ArtifactsV2(s) {
		if a.Name == name {
			return a.MaxBytes, true
		}
	}
	return 0, false
}

// UploadLimitV2 is the limit of a name that some /2 shape allows, for the upload service, which opens rows before it knows the
// shape of the document. The /1 allowlist (ArtifactMaxBytesFor) is unchanged; this only adds the /2 names.
func UploadLimitV2(name string) (int64, bool) {
	switch name {
	case AnalysisArtifact:
		return maxAnalysisBytes, true
	case StemVocalsArtifact:
		return maxStemBytes, true
	}
	for i := 0; i < MaxPlanSeeds; i++ {
		if name == PlanBundleName(i) {
			return MaxPlanBundleBytes, true
		}
	}
	return 0, false
}

// ExtraRequiredV2 lists the artifacts a finished output of the shape must report beyond the engine's own required set (which a
// render takes from the /1 table and the cot): analysis.json once the stage reported ok, and the bundle of every plan seed that
// reported ok. The result is sorted.
func ExtraRequiredV2(s KindShape, out Output) []string {
	var names []string
	switch s.kind() {
	case KindRender:
		if out.Ears != nil && out.Ears.Status == "ok" {
			names = append(names, AnalysisArtifact)
		}
	case KindAnalyze:
		names = append(names, AnalysisArtifact)
	case KindPlan:
		for _, p := range out.Plans {
			if p.Status == "ok" {
				names = append(names, PlanBundleName(p.Index))
			}
		}
	}
	slices.Sort(names)
	return names
}

// CheckOutputV2 checks a decoded /2 output against the shape of the document it answers: the kind, the presence of the ears and
// plans blocks, and the nullness of audio_seconds and truncated that C321.4 prescribes per kind. An error output only has to
// carry the right kind. The messages name fields, never values.
func CheckOutputV2(s KindShape, out Output) error {
	bad := func(field, rule string) error { return fmt.Errorf("%w: output: %s %s", ErrWorkerInput, field, rule) }
	if out.Contract != ContractV2 {
		return bad("contract", "must be "+ContractV2)
	}
	if out.Kind != s.kind() {
		return bad("kind", "does not match the document")
	}
	if out.Status != "ok" {
		return nil
	}
	switch s.kind() {
	case KindRender:
		if (out.Ears != nil) != s.Ears {
			return bad("ears", "must be present exactly when the document asked for the stage")
		}
		if out.AudioSeconds == nil {
			return bad("audio_seconds", "is required")
		}
	case KindAnalyze:
		if out.Ears == nil || out.Ears.Status != "ok" {
			return bad("ears.status", "must be ok on an ok analyze output")
		}
		if out.AudioSeconds == nil {
			return bad("audio_seconds", "is required")
		}
	case KindPlan:
		if len(out.Plans) != s.Seeds {
			return bad("plans", "must hold one entry per seed")
		}
		okSeeds := 0
		for i, p := range out.Plans {
			if p.Index != i {
				return bad("plans.index", "must count from zero in order")
			}
			if p.Status == "ok" {
				okSeeds++
			}
		}
		if okSeeds == 0 {
			return bad("plans", "needs at least one ok seed on an ok output")
		}
	}
	return nil
}
