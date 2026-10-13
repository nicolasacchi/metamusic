package ears

import (
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/ladder"
)

// nullableObject: see duels.nullableObject (the repo convention for objects that may be JSON null).
func nullableObject[T any](r huma.Registry) *huma.Schema {
	cp := *huma.SchemaFromType(r, reflect.TypeFor[T]())
	cp.Nullable = true
	return &cp
}

type (
	// SilenceBlockOrNull is a SilenceBlock that may be null.
	SilenceBlockOrNull SilenceBlock
	// ClippingBlockOrNull is a ClippingBlock that may be null.
	ClippingBlockOrNull ClippingBlock
	// TokenLoopsBlockOrNull is a TokenLoopsBlock that may be null.
	TokenLoopsBlockOrNull TokenLoopsBlock
	// StrengthOrNull is a ladder.Strength that may be null.
	StrengthOrNull ladder.Strength
)

type (
	silenceShape    SilenceBlock
	clippingShape   ClippingBlock
	tokenLoopsShape TokenLoopsBlock
	strengthShape   ladder.Strength
)

// Schema implements huma.SchemaProvider.
func (SilenceBlockOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableObject[silenceShape](r)
}

// Schema implements huma.SchemaProvider.
func (ClippingBlockOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableObject[clippingShape](r)
}

// Schema implements huma.SchemaProvider.
func (TokenLoopsBlockOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableObject[tokenLoopsShape](r)
}

// Schema implements huma.SchemaProvider.
func (StrengthOrNull) Schema(r huma.Registry) *huma.Schema { return nullableObject[strengthShape](r) }
