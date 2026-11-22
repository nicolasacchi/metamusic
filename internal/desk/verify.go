package desk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// Finding is one result of Verify (C357.4). Severity is "error" or "warning".
type Finding struct {
	Severity string
	Code     string // D1 .. D4 as desk_file_*, desk_score_*, desk_plan_job_*, desk_audition_*
	Subject  string
	Detail   string
}

// Finding codes.
const (
	CodeFileMissing    = "desk_file_missing"   // D1: a desk_plan_files file is absent
	CodeFileChanged    = "desk_file_changed"   // D1: its bytes do not hash to the row
	CodeScoreMismatch  = "desk_score_mismatch" // D2: a take plan's score is not the take's score
	CodePlanJobNoPlans = "desk_plan_job_empty" // D3: a completed plan job made no plan
	CodeAuditionFile   = "desk_audition_file"  // D4: an audition's MP3 is missing (warning: rebuilt on request)
)

// Verify checks the Desk's rows against the files and the library (C357.4):
//
//	D1 every desk_plan_files row's file exists with its sha256 (error);
//	D2 a take plan's score_sha256 equals its take's scores.abc_sha256 (error);
//	D3 every completed plan job has at least one desk_plans row (error);
//	D4 an audition row whose file is missing (warning, rebuilt on the next request).
//
// It reads only; db may be the read pool.
func Verify(ctx context.Context, db *sql.DB, root *os.Root) ([]Finding, error) {
	var out []Finding
	rows, err := db.QueryContext(ctx, `SELECT plan_id, name, sha256, path FROM desk_plan_files ORDER BY plan_id, name`)
	if err != nil {
		return nil, err
	}
	type fileRowV struct{ plan, name, sha, path string }
	var files []fileRowV
	for rows.Next() {
		var f fileRowV
		if err := rows.Scan(&f.plan, &f.name, &f.sha, &f.path); err != nil {
			_ = rows.Close()
			return nil, err
		}
		files = append(files, f)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	for _, f := range files {
		subject := f.plan + "/" + f.name
		sum, err := hashFile(root, f.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out = append(out, Finding{"error", CodeFileMissing, subject, "the file of the plan is missing"})
		case err != nil:
			return nil, err
		case sum != f.sha:
			out = append(out, Finding{"error", CodeFileChanged, subject, "the file does not match its recorded sha256"})
		}
	}
	mm, err := db.QueryContext(ctx, `SELECT p.id, p.track_id FROM desk_plans p JOIN scores s ON s.track_id = p.track_id
		WHERE p.source = 'take' AND p.score_sha256 <> s.abc_sha256 ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	for mm.Next() {
		var id, track string
		if err := mm.Scan(&id, &track); err != nil {
			_ = mm.Close()
			return nil, err
		}
		out = append(out, Finding{"error", CodeScoreMismatch, id, "the plan's score differs from the score of take " + track})
	}
	if err := closeRows(mm); err != nil {
		return nil, err
	}
	jr, err := db.QueryContext(ctx, `SELECT j.id FROM jobs j WHERE j.kind = 'plan' AND j.state = 'completed'
		AND NOT EXISTS (SELECT 1 FROM desk_plans p WHERE p.job_id = j.id) ORDER BY j.id`)
	if err != nil {
		return nil, err
	}
	for jr.Next() {
		var id string
		if err := jr.Scan(&id); err != nil {
			_ = jr.Close()
			return nil, err
		}
		out = append(out, Finding{"error", CodePlanJobNoPlans, id, "a completed plan job has no plan"})
	}
	if err := closeRows(jr); err != nil {
		return nil, err
	}
	ar, err := db.QueryContext(ctx, `SELECT id, mp3_path FROM desk_auditions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	type aud struct{ id, path string }
	var auds []aud
	for ar.Next() {
		var a aud
		if err := ar.Scan(&a.id, &a.path); err != nil {
			_ = ar.Close()
			return nil, err
		}
		auds = append(auds, a)
	}
	if err := closeRows(ar); err != nil {
		return nil, err
	}
	for _, a := range auds {
		if fi, err := root.Stat(a.path); err != nil || !fi.Mode().IsRegular() {
			out = append(out, Finding{"warning", CodeAuditionFile, a.id, "the audition file is missing and is rebuilt on the next request"})
		}
	}
	return out, nil
}

func hashFile(root *os.Root, rel string) (string, error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("desk: read %s: %w", rel, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
