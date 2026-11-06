package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// E2 views (C148.3, C157.1, C166). The shapes are frozen by the contract freeze (E2-F2). The readers below are complete: a
// take without stems or lrc answers an empty list and null. Stem artifacts are written by E2b's attach ingest (jobs/ingest_attach.go).

// EngineACEStep is recipes.engine of an ACE-Step 1.5 recipe.
const EngineACEStep = "acestep15"

// RecipeACE is the engine-native part of an acestep15 recipe as the take view shows it: what the UI and agents act on. The rest
// of the canonical request (instrumental, vocal language, thinking, steps, guidance, engine_params_version, lrc) is in the
// take's request.json artifact; Recipe.Cot already says whether the planner ran.
type RecipeACE struct {
	Task          string      `json:"task" enum:"text2music,repaint,extend,complete,extract"`
	TaskClass     string      `json:"task_class" enum:"turbo,base"`
	BPM           *int        `json:"bpm"`
	KeyScale      *string     `json:"keyscale"`
	TimeSignature *string     `json:"timesignature"`
	DurationS     *float64    `json:"duration_s"`
	Src           *RecipeSrc  `json:"src,omitempty"`
	Ref           *RecipeRef  `json:"ref,omitempty"`
	Edit          *RecipeEdit `json:"edit,omitempty"`
}

// RecipeSrc pins the source audio of an edit.
type RecipeSrc struct {
	TrackID      string  `json:"track_id"`
	MasterSHA256 string  `json:"master_sha256"`
	DurationS    float64 `json:"duration_s"`
}

// RecipeRef pins the reference clip of a render.
type RecipeRef struct {
	TrackID    string  `json:"track_id"`
	ClipSHA256 string  `json:"clip_sha256"`
	FromS      float64 `json:"from_s"`
	ToS        float64 `json:"to_s"`
}

// RecipeEdit holds the mode inputs of an edit, in seconds.
type RecipeEdit struct {
	FromS   *float64 `json:"from_s"`
	ToS     *float64 `json:"to_s"`
	ExtendS *float64 `json:"extend_s"`
	Tracks  []string `json:"tracks" nullable:"false"`
}

// StemInfo is one stem artifact of a take (artifacts name stem-<name>[-<n>].flac).
type StemInfo struct {
	Name     string `json:"name"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
}

// LRCInfo is the lyrics.lrc artifact of a take.
type LRCInfo struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// StemName extracts the stem name of an artifact name stem-<name>.flac or stem-<name>-<n>.flac; ok is false for any other
// name.
func StemName(artifact string) (name string, ok bool) {
	rest, found := strings.CutPrefix(artifact, "stem-")
	if !found {
		return "", false
	}
	rest, found = strings.CutSuffix(rest, ".flac")
	if !found || rest == "" {
		return "", false
	}
	if i := strings.LastIndexByte(rest, '-'); i > 0 {
		if _, err := strconv.Atoi(rest[i+1:]); err == nil {
			rest = rest[:i]
		}
	}
	return rest, true
}

// Stems lists the stem artifacts of a take, sorted by artifact name.
func (s *Service) Stems(ctx context.Context, trackID string) ([]StemInfo, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT name, sha256, bytes FROM artifacts WHERE track_id = ? AND name GLOB 'stem-*.flac' ORDER BY name`, trackID)
	if err != nil {
		return nil, fmt.Errorf("library: stems: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []StemInfo{}
	for rows.Next() {
		var a StemInfo
		if err := rows.Scan(&a.Artifact, &a.SHA256, &a.Bytes); err != nil {
			return nil, err
		}
		n, ok := StemName(a.Artifact)
		if !ok {
			continue
		}
		a.Name = n
		out = append(out, a)
	}
	return out, rows.Err()
}

// LRC returns the lyrics.lrc artifact of a take, nil when it has none.
func (s *Service) LRC(ctx context.Context, trackID string) (*LRCInfo, error) {
	var l LRCInfo
	switch err := s.st.R.QueryRowContext(ctx, `SELECT sha256, bytes FROM artifacts WHERE track_id = ? AND name = 'lyrics.lrc'`, trackID).Scan(&l.SHA256, &l.Bytes); {
	case err == nil:
		return &l, nil
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	default:
		return nil, fmt.Errorf("library: lrc: %w", err)
	}
}

func (s *Service) e2Extras(ctx context.Context, d *TrackDetail) error {
	var err error
	if d.Stems, err = s.Stems(ctx, d.ID); err != nil {
		return err
	}
	if d.LRC, err = s.LRC(ctx, d.ID); err != nil {
		return err
	}
	if d.Recipe.Engine != EngineACEStep {
		return nil
	}
	var raw string
	if err := s.st.R.QueryRowContext(ctx, `SELECT request_json FROM recipes WHERE id = ?`, d.Recipe.ID).Scan(&raw); err != nil {
		return fmt.Errorf("library: recipe request: %w", err)
	}
	d.Recipe.ACE = recipeACE(raw)
	return nil
}

// recipeACE reads the engine-native fields from the canonical request without importing the engine package.
func recipeACE(raw string) *RecipeACE {
	var r struct {
		Task          string  `json:"task"`
		Instrumental  bool    `json:"instrumental"`
		VocalLanguage string  `json:"vocal_language"`
		BPM           *int    `json:"bpm"`
		KeyScale      *string `json:"keyscale"`
		TimeSignature *string `json:"timesignature"`
		DurationDS    int     `json:"duration_ds"`
		Thinking      bool    `json:"thinking"`
		LRC           bool    `json:"lrc"`
		Steps         int     `json:"inference_steps"`
		GuidanceMilli *int    `json:"guidance_milli"`
		ParamsVersion string  `json:"engine_params_version"`
		Src           *struct {
			TrackID      string `json:"track_id"`
			MasterSHA256 string `json:"master_sha256"`
			DurationDS   int    `json:"duration_ds"`
		} `json:"src"`
		Ref *struct {
			TrackID    string `json:"track_id"`
			ClipSHA256 string `json:"clip_sha256"`
			FromDS     int    `json:"from_ds"`
			ToDS       int    `json:"to_ds"`
		} `json:"ref"`
		RepaintFromDS *int     `json:"repaint_from_ds"`
		RepaintToDS   *int     `json:"repaint_to_ds"`
		ExtendDS      *int     `json:"extend_ds"`
		Tracks        []string `json:"tracks"`
	}
	if json.Unmarshal([]byte(raw), &r) != nil {
		return nil
	}
	sec := func(ds *int) *float64 {
		if ds == nil {
			return nil
		}
		v := float64(*ds) / 10
		return &v
	}
	out := &RecipeACE{Task: r.Task, TaskClass: "turbo", BPM: r.BPM, KeyScale: r.KeyScale, TimeSignature: r.TimeSignature}
	if r.Task == "complete" || r.Task == "extract" {
		out.TaskClass = "base"
	}
	if r.DurationDS > 0 {
		out.DurationS = sec(&r.DurationDS)
	}
	if r.Src != nil {
		out.Src = &RecipeSrc{TrackID: r.Src.TrackID, MasterSHA256: r.Src.MasterSHA256, DurationS: float64(r.Src.DurationDS) / 10}
	}
	if r.Ref != nil {
		out.Ref = &RecipeRef{TrackID: r.Ref.TrackID, ClipSHA256: r.Ref.ClipSHA256, FromS: float64(r.Ref.FromDS) / 10, ToS: float64(r.Ref.ToDS) / 10}
	}
	if r.RepaintFromDS != nil || r.RepaintToDS != nil || r.ExtendDS != nil || len(r.Tracks) > 0 {
		e := &RecipeEdit{FromS: sec(r.RepaintFromDS), ToS: sec(r.RepaintToDS), ExtendS: sec(r.ExtendDS), Tracks: r.Tracks}
		if e.Tracks == nil {
			e.Tracks = []string{}
		}
		out.Edit = e
	}
	return out
}
