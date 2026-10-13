package duels

import (
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// nullableObject renders T (a method-less shadow of the real type) as an
// inline object schema with Nullable=true (the repo convention, see
// library.nullableObject): Huma refuses nullable fields that point at a $ref
// object, so the sub-objects that really can be null provide their own schema.
func nullableObject[T any](r huma.Registry) *huma.Schema {
	cp := *huma.SchemaFromType(r, reflect.TypeFor[T]())
	cp.Nullable = true
	return &cp
}

// The OrNull types are the same structs under another name, used behind a
// pointer where the field may be JSON null. They marshal exactly like their
// base type.
type (
	// TrackBriefOrNull is a TrackBrief that may be null.
	TrackBriefOrNull TrackBrief
	// VerdictOrNull is a Verdict that may be null.
	VerdictOrNull Verdict
	// HumanVerdictBriefOrNull is a HumanVerdictBrief that may be null.
	HumanVerdictBriefOrNull HumanVerdictBrief
	// DuelOrNull is a Duel that may be null.
	DuelOrNull Duel
)

type (
	trackBriefShape        TrackBrief
	verdictShape           Verdict
	humanVerdictBriefShape HumanVerdictBrief
	duelShape              Duel
)

// Schema implements huma.SchemaProvider.
func (TrackBriefOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableObject[trackBriefShape](r)
}

// Schema implements huma.SchemaProvider.
func (VerdictOrNull) Schema(r huma.Registry) *huma.Schema { return nullableObject[verdictShape](r) }

// Schema implements huma.SchemaProvider.
func (HumanVerdictBriefOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableObject[humanVerdictBriefShape](r)
}

// Schema implements huma.SchemaProvider.
func (DuelOrNull) Schema(r huma.Registry) *huma.Schema { return nullableObject[duelShape](r) }
