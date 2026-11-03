package library

// TransformRef marks a Take that is the output of a transform (T1, D-T1-4): the kind, the transform id (equal to the Take id)
// and the id-less spec hash.
type TransformRef struct {
	Kind        string `json:"kind"`
	TransformID string `json:"transform_id"`
	SpecHash    string `json:"spec_hash"`
}
