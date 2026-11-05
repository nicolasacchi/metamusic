package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// TransformInfo is what /context says about a transform Take (C211.4): the kind, the frozen parameters, the inputs with
// their roles, where each alignment number came from and the loudness target. Everything is read from the frozen spec
// (recipes.request_json) and transform_inputs; no audio and no other table is touched.
type TransformInfo struct {
	Kind        string
	TransformID string
	SpecHash    string
	Title       string
	Params      json.RawMessage
	Inputs      []TransformInputInfo
	Alignment   []AlignSourceInfo
	TargetLUFS  *float64
}

// TransformInputInfo is one input of a transform: ord 0 is the ledger parent.
type TransformInputInfo struct {
	Ord     int
	Role    string
	TrackID string
}

// AlignSourceInfo is the alignment resolution of one input, sources only (the numbers are in the spec).
type AlignSourceInfo struct {
	Ord       int
	BPM       float64
	BPMSource string
	KeySource string
}

// TransformOf reads the transform block of a Take; nil (and no error) when the Take is not a transform output.
func (s *Service) TransformOf(ctx context.Context, trackID string) (*TransformInfo, error) {
	var ti TransformInfo
	var spec string
	err := s.st.R.QueryRowContext(ctx, `SELECT x.id, x.kind, x.spec_hash, x.title, r.request_json
		FROM tracks t JOIN transforms x ON x.id = t.transform_id JOIN recipes r ON r.id = x.recipe_id WHERE t.id = ?`, trackID).
		Scan(&ti.TransformID, &ti.Kind, &ti.SpecHash, &ti.Title, &spec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("library: transform of %s: %w", trackID, err)
	}
	var doc struct {
		Params     json.RawMessage `json:"params"`
		TargetLUFS *float64        `json:"target_lufs"`
		Alignment  *struct {
			Inputs []struct {
				BPM       float64 `json:"bpm"`
				BPMSource string  `json:"bpm_source"`
				KeySource string  `json:"key_source"`
			} `json:"inputs"`
		} `json:"alignment"`
	}
	if json.Unmarshal([]byte(spec), &doc) != nil { // an unreadable spec still gets its kind and inputs
		doc.Params, doc.TargetLUFS, doc.Alignment = nil, nil, nil
	}
	ti.Params, ti.TargetLUFS = doc.Params, doc.TargetLUFS
	if doc.Alignment != nil {
		for i, a := range doc.Alignment.Inputs {
			ti.Alignment = append(ti.Alignment, AlignSourceInfo{Ord: i, BPM: a.BPM, BPMSource: a.BPMSource, KeySource: a.KeySource})
		}
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT ord, role, track_id FROM transform_inputs WHERE transform_id = ? ORDER BY ord`, ti.TransformID)
	if err != nil {
		return nil, fmt.Errorf("library: transform inputs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var in TransformInputInfo
		if err := rows.Scan(&in.Ord, &in.Role, &in.TrackID); err != nil {
			return nil, err
		}
		ti.Inputs = append(ti.Inputs, in)
	}
	return &ti, rows.Err()
}

// transformRef is the transform block of a Take read from the transforms join; nil for a Take without one.
func transformRef(id string, kind, hash sql.NullString) *TransformRef {
	if !kind.Valid {
		return nil
	}
	return &TransformRef{Kind: kind.String, TransformID: id, SpecHash: hash.String}
}
