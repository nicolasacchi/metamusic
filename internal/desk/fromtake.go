package desk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// Reasons of PLAN_SOURCE_UNSUPPORTED (C358.1).
const (
	ReasonEngine    = "engine"
	ReasonReference = "reference"
	ReasonLossy     = "lossy"
	ReasonTransform = "transform"
	ReasonNoScore   = "no_score"
	ReasonTooLarge  = "too_large"
)

func unsupported(reason, msg string) error {
	return problem.E(http.StatusUnprocessableEntity, CodeSourceUnsupported, msg, map[string]any{"reason": reason})
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// scoreInvalid is the 422 of a score that does not parse.
func scoreInvalid(err error) error {
	d := map[string]any{"line": 1, "col": 1, "rule": "internal"}
	var se *abc.SyntaxError
	if errors.As(err, &se) {
		d["line"], d["col"], d["rule"] = se.Line, se.Col, se.Rule
	}
	return problem.E(http.StatusUnprocessableEntity, CodeScoreInvalid, "the score does not parse", d)
}

// FromTake makes the plan of a take's score (D-DESK-1): the take plan is one per take and points at takes/<id>/score.abc without a
// copy. created is false when the plan already existed.
func (s *Service) FromTake(ctx context.Context, p auth.Principal, trackID string) (Plan, bool, error) {
	if err := s.wired(); err != nil {
		return Plan{}, false, err
	}
	var (
		title, importRule, engine, reqJSON, cot string
		lossy                                   bool
		transformID, scorePath, scoreSHA, q     sql.NullString
		trunc                                   sql.NullInt64
		durS                                    float64
		seed                                    int64
		bars                                    sql.NullInt64
		meter                                   sql.NullString
		spb                                     sql.NullFloat64
		aligned                                 sql.NullInt64
	)
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT t.title, t.import_rule, t.lossy, t.transform_id, t.truncated_abc, t.duration_s,
		r.engine, r.request_json, r.seed, r.cot, sc.abc_path, sc.abc_sha256, sc.bars, sc.meter, sc.q, sc.seconds_per_bar, sc.aligned
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id LEFT JOIN scores sc ON sc.track_id = t.id WHERE t.id = ?`, trackID).Scan(
		&title, &importRule, &lossy, &transformID, &trunc, &durS, &engine, &reqJSON, &seed, &cot, &scorePath, &scoreSHA, &bars, &meter, &q, &spb, &aligned)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, false, notFound("take")
	}
	if err != nil {
		return Plan{}, false, err
	}
	switch {
	case lossy:
		return Plan{}, false, unsupported(ReasonLossy, "a lossy import has no score of its own")
	case importRule == "reference" || engine == "unknown":
		return Plan{}, false, unsupported(ReasonReference, "a reference take has no recipe, so no plan")
	case transformID.Valid:
		return Plan{}, false, unsupported(ReasonTransform, "a transform take is not a render of a score")
	case engine != "yue2":
		return Plan{}, false, unsupported(ReasonEngine, "only yue2 takes have plans")
	case !scorePath.Valid || (cot != "full" && cot != "melody"):
		return Plan{}, false, unsupported(ReasonNoScore, "the take has no score or its recipe did not plan one")
	}
	if id, ok, err := s.takePlan(ctx, trackID); err != nil {
		return Plan{}, false, err
	} else if ok {
		pl, err := s.Get(ctx, p, id, DefaultInclude)
		return pl, false, err
	}
	src, err := s.readScore(scorePath.String)
	if err != nil {
		if errors.Is(err, errScoreTooLarge) {
			return Plan{}, false, unsupported(ReasonTooLarge, "the score is over 64 KiB")
		}
		return Plan{}, false, err
	}
	sum := sha256Hex(src)
	if sum != scoreSHA.String {
		return Plan{}, false, fmt.Errorf("desk: the score of take %s does not match its recorded hash", trackID)
	}
	tune, err := abc.Parse(src, abc.Options{Dialect: abc.DialectYuE2})
	if err != nil {
		return Plan{}, false, scoreInvalid(err)
	}
	var req yue2.Request
	if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
		return Plan{}, false, fmt.Errorf("desk: recipe of take %s: %w", trackID, err)
	}
	req.ABC = nil
	prof := profileOf(tune, int(durS+0.999))
	// scores rows are the timing source of a take (D-DESK-10): their values win over the tune's own reading
	if bars.Valid {
		prof.bars = int(bars.Int64)
	}
	if meter.Valid {
		prof.meter = meter.String
	}
	prof.q = strp(q)
	if spb.Valid {
		v := spb.Float64
		prof.spb = &v
	}
	var truncated *bool
	if trunc.Valid {
		v := trunc.Int64 == 1
		truncated = &v
	}
	n := newPlan{id: s.o.NewID(), source: "take", trackID: trackID, baseTrackID: trackID, requestJSON: string(yue2.CanonicalJSON(req)),
		seed: seed, cot: cot, score: src, sha: sum, scorePath: scorePath.String, dialect: string(abc.DialectYuE2), prof: prof,
		aligned: aligned.Int64 == 1, truncated: truncated, title: title, by: p}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.insert(ctx, tx, n); err != nil {
		_ = tx.Rollback()
		// a concurrent request made the plan first
		if id, ok, qerr := s.takePlan(ctx, trackID); qerr == nil && ok {
			pl, gerr := s.Get(ctx, p, id, DefaultInclude)
			return pl, false, gerr
		}
		return Plan{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, false, err
	}
	audit.NoteCreated(ctx, n.id)
	pl, err := s.Get(ctx, p, n.id, DefaultInclude)
	return pl, true, err
}

func (s *Service) takePlan(ctx context.Context, trackID string) (string, bool, error) {
	var id string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT id FROM desk_plans WHERE source = 'take' AND track_id = ?`, trackID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return id, true, nil
}
