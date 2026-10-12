package library

import (
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// nullableObject renders T (a method-less shadow of the real type) as an inline
// object schema with Nullable=true (SchemaFromType returns a struct inline, never a $ref), i.e. `type: ["object","null"]`. Huma refuses
// nullable fields that point at a $ref object, so the sub-objects that really
// can be null (pointer fields) provide their own inline schema.
func nullableObject[T any](r huma.Registry) *huma.Schema {
	cp := *huma.SchemaFromType(r, reflect.TypeFor[T]())
	cp.Nullable = true
	return &cp
}

type (
	truncationShape Truncation
	scoreShape      ScoreInfo
	mp3Shape        MP3Info
	loudnessShape   LoudnessOut
	spectrumShape   SpectrumOut
	draftSrcShape   DraftSource
)

// Schema implements huma.SchemaProvider.
func (Truncation) Schema(r huma.Registry) *huma.Schema { return nullableObject[truncationShape](r) }

// Schema implements huma.SchemaProvider.
func (ScoreInfo) Schema(r huma.Registry) *huma.Schema { return nullableObject[scoreShape](r) }

// Schema implements huma.SchemaProvider.
func (MP3Info) Schema(r huma.Registry) *huma.Schema { return nullableObject[mp3Shape](r) }

// Schema implements huma.SchemaProvider.
func (LoudnessOut) Schema(r huma.Registry) *huma.Schema { return nullableObject[loudnessShape](r) }

// Schema implements huma.SchemaProvider.
func (SpectrumOut) Schema(r huma.Registry) *huma.Schema { return nullableObject[spectrumShape](r) }

// Schema implements huma.SchemaProvider.
func (DraftSource) Schema(r huma.Registry) *huma.Schema { return nullableObject[draftSrcShape](r) }
