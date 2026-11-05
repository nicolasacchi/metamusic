package librarytest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TransformTake describes one transform Take for InsertTransformTake. Parent is the ledger parent (empty for score_render);
// Also lists the further inputs (role -> track id) recorded as transform_inputs rows from ord 1.
type TransformTake struct {
	N      int // distinct per Take of one database
	Kind   string
	Parent string
	// ParentRole is the role of the ord-0 input (default "source"; "a" for a morph).
	ParentRole string
	Also       []TransformInput
	Age        time.Duration // how long before now the Take was recorded (default: now)
}

// TransformInput is a further input of a transform.
type TransformInput struct{ Role, Track string }

// InsertTransformTake writes the whole transform write path in one transaction (recipe, transform row, input rows, Take)
// and returns the Take id. The recipe is a spec-only dsp recipe (empty style and lyrics), the Take 24-bit stereo.
func InsertTransformTake(tb testing.TB, st *store.Store, now time.Time, tt TransformTake) string {
	tb.Helper()
	ctx := context.Background()
	svc := library.New(st, nil, func() time.Time { return now })
	id := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd50000+tt.N)
	svc.UseIDFunc(func() string { return fmt.Sprintf("01900000-0000-7000-8001-%012x", 0xd50000+tt.N) })
	specID := fmt.Sprintf("dsp-%s-%020d", tt.Kind, tt.N)
	raw := `{"dsp_version":"mm-dsp/1","id":"` + specID + `","kind":"` + tt.Kind + `","seed":0,"v":1}`
	rh := sha256.Sum256([]byte(raw))
	specHash := sha256.Sum256([]byte("spec/" + specID))
	tx := begin(tb, st)
	rid, _, err := svc.InsertTransformRecipe(ctx, tx, library.SpecRecipe{Kind: tt.Kind, Canonical: []byte(raw), Hash: hex.EncodeToString(rh[:]), RequestID: specID}, "agent:tok1")
	check(tb, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO transforms(id,kind,recipe_id,spec_hash,title,principal_id,principal_kind,principal_name,state,expected_cpu_ms,max_cpu_ms,out_seconds_est,mem_mb_est,created_at)
		VALUES(?,?,?,?,?,'agent:tok1','agent','tok1','queued',1,1,1.0,1,?)`, id, tt.Kind, rid, hex.EncodeToString(specHash[:]), "Synthetic "+tt.Kind+" "+fmt.Sprint(tt.N), store.FormatTime(now))
	check(tb, err)
	inputs := []TransformInput{}
	if tt.Parent != "" {
		role := tt.ParentRole
		if role == "" {
			role = "source"
		}
		inputs = append(inputs, TransformInput{role, tt.Parent})
	}
	inputs = append(inputs, tt.Also...)
	for i, in := range inputs {
		_, err = tx.ExecContext(ctx, `INSERT INTO transform_inputs(transform_id,ord,role,track_id,master_sha256) SELECT ?,?,?,id,master_sha256 FROM tracks WHERE id = ?`, id, i, in.Role, in.Track)
		check(tb, err)
	}
	title := "Synthetic Transform X" + fmt.Sprint(tt.N)
	slug := strings.ToLower(strings.ReplaceAll(title, " ", "-"))
	h := sha([]byte("t1-transform-take/" + id))
	nt := library.NewTake{
		ID: id, Title: title, Slug: slug, RecipeID: rid, Origin: "transform", ImportRule: "transform", TransformID: id, PrincipalID: "agent:tok1",
		RecordedAt: now.Add(-tt.Age), DurationS: 10, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 480000,
		MasterPath: "audio/masters/" + slug + "__" + h[:8] + ".flac", MasterSHA256: h, MasterBytes: 64,
		SourceSHA256: sha([]byte("t1-tsrc/" + id)), FramesSHA256: sha([]byte("t1-tframes/" + id)), AudioMD5: h[:32],
		Loudness: analysis.Loudness{IntegratedLUFS: -16, LRA: 3, LRALowLUFS: -18, LRAHighLUFS: -14, TruePeakDBTP: -3, SamplePeakDBFS: -3.3,
			ShortTermMaxLUFS: -15, ShortTerm1Hz: []float64{-16, -16, -16, -16, -16, -16, -16, -16, -16, -16}},
		Spectrum:    analysis.Spectrum{BandwidthHz: bptrInt(16000), RefDB: -40, ThresholdDB: -110, Frames: 30},
		ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -2,
		Renditions: []library.NewRendition{{Kind: "peaks", SHA256: sha([]byte("t1-tpeaks/" + id)), Bytes: 32, Path: "derived/t1/" + h[:16] + ".peaks.json", Params: map[string]any{"format": analysis.PeaksFormat}}},
		Mutation:   &library.NewMutation{Operator: tt.Kind, Rationale: "synthetic", Params: map[string]any{}},
	}
	if tt.Parent != "" {
		nt.ParentTrackID, nt.RootTrackID = tt.Parent, tt.Parent
		var gen int
		var root string
		check(tb, tx.QueryRowContext(ctx, `SELECT generation, root_track_id FROM tracks WHERE id = ?`, tt.Parent).Scan(&gen, &root))
		nt.Generation, nt.RootTrackID = gen+1, root
	}
	check(tb, svc.InsertTake(ctx, tx, nt))
	check(tb, tx.Commit())
	return id
}
