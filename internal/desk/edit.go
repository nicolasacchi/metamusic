package desk

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// EditIn is an edit of a plan (C358.2).
type EditIn struct {
	Ops       []abc.Op
	Rationale string
}

// MaxRationale is the longest edit rationale.
const MaxRationale = 1000

func editInvalid(msg string, d map[string]any) error {
	return problem.E(http.StatusUnprocessableEntity, CodeEditInvalid, msg, d)
}

// Edit applies bar edits to a plan and stores the result as a child plan; the parent never changes (D-DESK-1). A violation of the
// structural invariants (D-DESK-12) or a malformed edit is 422 PLAN_EDIT_INVALID, identical bytes 422 PLAN_NO_CHANGE, and an
// existing child with the same bytes is returned with created false.
func (s *Service) Edit(ctx context.Context, p auth.Principal, planID string, in EditIn) (Plan, bool, error) {
	if err := s.wired(); err != nil {
		return Plan{}, false, err
	}
	if len([]rune(in.Rationale)) > MaxRationale {
		return Plan{}, false, invalid("rationale", "the rationale is longer than 1000 characters")
	}
	parent, err := load(ctx, s.o.Store.R, planID)
	if err != nil {
		return Plan{}, false, err
	}
	src, err := s.readScore(parent.scorePath)
	if err != nil {
		return Plan{}, false, err
	}
	o := abc.Options{Dialect: abc.Dialect(parent.dialect)}
	out, edited, viol, err := abc.Edit(src, nil, in.Ops, o)
	if err != nil {
		var ee *abc.EditError
		if errors.As(err, &ee) {
			d := map[string]any{"rule": ee.Rule, "op": ee.Op, "msg": ee.Msg}
			if ee.Rule == "result_unparsable" || ee.Rule == "source_unparsable" {
				d["line"], d["col"] = ee.Line, ee.Col
			}
			return Plan{}, false, editInvalid("the edit is not valid", d)
		}
		return Plan{}, false, err
	}
	if len(viol) > 0 {
		return Plan{}, false, editInvalid("the edit changes the structure of the score", map[string]any{"rule": "invariants", "violations": viol})
	}
	if len(out) > MaxScoreBytes {
		return Plan{}, false, editInvalid("the edited score is over 64 KiB", map[string]any{"rule": "too_large"})
	}
	if bytes.Equal(out, src) {
		return Plan{}, false, problem.E(http.StatusUnprocessableEntity, CodeNoChange, "the edit leaves the score as it is", nil)
	}
	sum := sha256Hex(out)
	if id, ok, err := s.childWith(ctx, planID, sum); err != nil {
		return Plan{}, false, err
	} else if ok {
		pl, err := s.Get(ctx, p, id, DefaultInclude)
		return pl, false, err
	}
	var rationale *string
	if r := strings.TrimSpace(in.Rationale); r != "" {
		rationale = &r
	}
	id := s.o.NewID()
	rel := plansRoot + "/" + id + "/" + scoreName
	n := newPlan{id: id, source: "edit", parentID: planID, baseTrackID: deref(parent.Base.TrackID), baseDraftID: deref(parent.Base.DraftID),
		requestJSON: parent.requestJSON, seed: parent.Seed, cot: parent.Cot, score: out, sha: sum, scorePath: rel, dialect: parent.dialect,
		prof: profileOf(edited, parent.ExpectedAudioS), aligned: parent.aligned, truncated: parent.Truncated,
		edit: &EditInfo{Ops: in.Ops, Rationale: rationale}, title: parent.Title, by: p,
		files: []fileRow{{name: scoreName, sha: sum, path: rel, bytes: len(out)}}}
	if err := s.writePlanDir(id, map[string][]byte{scoreName: out}); err != nil {
		return Plan{}, false, err
	}
	if err := s.commitNew(ctx, n); err != nil {
		s.removePlanDir(id)
		// a concurrent identical edit won
		if cid, ok, qerr := s.childWith(ctx, planID, sum); qerr == nil && ok {
			pl, gerr := s.Get(ctx, p, cid, DefaultInclude)
			return pl, false, gerr
		}
		return Plan{}, false, err
	}
	audit.NoteCreated(ctx, id)
	pl, err := s.Get(ctx, p, id, DefaultInclude)
	return pl, true, err
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Service) commitNew(ctx context.Context, n newPlan) error {
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.insert(ctx, tx, n); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) childWith(ctx context.Context, parent, sha string) (string, bool, error) {
	var id string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT id FROM desk_plans WHERE source = 'edit' AND parent_plan_id = ? AND score_sha256 = ?`, parent, sha).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return id, true, nil
}

// writePlanDir writes the files of a plan into plans/<id>/ (files 0444, directory 0755 like every other data directory: a read-only
// directory would make a data dir impossible to remove with rm -rf or t.TempDir, and the files are what is frozen). The files are written into a partial
// directory and renamed into place, so a crash leaves either nothing or a complete directory; a partial or unclaimed directory is
// removed by the sweep (C357.2).
func (s *Service) writePlanDir(id string, files map[string][]byte) error {
	r := s.o.Data
	if err := r.MkdirAll(plansRoot, 0o755); err != nil {
		return fmt.Errorf("desk: create %s: %w", plansRoot, unwrapPath(err))
	}
	tmp := plansRoot + "/" + id + partialSuffix
	if err := r.Mkdir(tmp, 0o700); err != nil {
		return fmt.Errorf("desk: create plan dir: %w", unwrapPath(err))
	}
	fail := func(err error) error {
		_ = r.Chmod(tmp, 0o700)
		_ = r.RemoveAll(tmp)
		return err
	}
	for name, b := range files {
		f, err := r.OpenFile(tmp+"/"+name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
		if err != nil {
			return fail(fmt.Errorf("desk: write %s: %w", name, unwrapPath(err)))
		}
		_, werr := f.Write(b)
		serr := f.Sync()
		cerr := f.Close()
		if err := errors.Join(werr, serr, cerr); err != nil {
			return fail(fmt.Errorf("desk: write %s: %w", name, unwrapPath(err)))
		}
	}
	if err := r.Chmod(tmp, 0o755); err != nil {
		return fail(fmt.Errorf("desk: chmod plan dir: %w", unwrapPath(err)))
	}
	if err := r.Rename(tmp, plansRoot+"/"+id); err != nil {
		return fail(fmt.Errorf("desk: place plan dir: %w", unwrapPath(err)))
	}
	return nil
}

// removePlanDir removes a plan directory whose row was never stored.
func (s *Service) removePlanDir(id string) {
	rel := plansRoot + "/" + id
	if _, err := s.o.Data.Stat(rel); err != nil && errors.Is(err, fs.ErrNotExist) {
		return
	}
	_ = s.o.Data.Chmod(rel, 0o700)
	_ = s.o.Data.RemoveAll(rel)
}

const partialSuffix = ".partial"
