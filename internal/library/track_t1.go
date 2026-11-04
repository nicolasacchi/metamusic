package library

import "github.com/danielgtaylor/huma/v2"

// TransformRef marks a Take that is the output of a transform (T1, D-T1-4): the kind, the transform id (equal to the Take id)
// and the id-less spec hash.
type TransformRef struct {
	Kind        string `json:"kind"`
	TransformID string `json:"transform_id"`
	SpecHash    string `json:"spec_hash"`
}

type transformRefShape TransformRef

// Schema implements huma.SchemaProvider: the block is an inline object that may be null (Huma refuses a nullable $ref).
func (TransformRef) Schema(r huma.Registry) *huma.Schema { return nullableObject[transformRefShape](r) }
