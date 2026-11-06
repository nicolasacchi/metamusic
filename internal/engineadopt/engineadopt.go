// Package engineadopt reads and appends the engine adoption record (D-E2-15, table engine_adoption_log, view
// engine_adoption): an engine other than yue2 receives money only after it is adopted. In E2a and E2b the record is written by
// the operator CLI (`metamusic engines adopt|reject`, via 'cli'); the blind-duel engine trial of E2c appends 'trial' rows
// through the same table.
//
// The contract freeze (E2-F2) fixed this surface; lane E2-OP fills the bodies. generation consumes Service through its
// AdoptionGate interface (Admitted), the engine operations through State and List, the CLI through Adopt and Reject.
package engineadopt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/canonjson"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// YuE2 is the engine that is always adopted (seeded by the migration, and honoured here even without a row).
const YuE2 = "yue2"

// Errors of Adopt and Reject. ErrReasonRequired: the reason is 1..500 characters. ErrUnknownEngine: the engine is yue2 (its
// row is the seed and is never rewritten by the CLI) or is not a name the caller registered.
var (
	ErrReasonRequired = errors.New("engineadopt: a reason of 1 to 500 characters is required")
	ErrByRequired     = errors.New("engineadopt: the operator name is required")
	ErrYuE2Fixed      = errors.New("engineadopt: yue2 is adopted by the seed and cannot be changed here")
	ErrBadEngine      = errors.New("engineadopt: engine name is empty or not a plain identifier")
)

// AdoptHint is the text list_engines and ENGINE_NOT_ADOPTED show: how an operator adopts the engine. It is text only; no
// agent path executes it.
func AdoptHint(engine string) string {
	return "metamusic engines adopt " + engine + " --reason \"...\""
}

// States of an engine's adoption.
const (
	StateAdopted    = "adopted"
	StateNotAdopted = "not_adopted"
	StateNone       = "none" // no log row: counts as not adopted (C158.5)
)

// Ways a row got into the log.
const (
	ViaSeed  = "seed"
	ViaCLI   = "cli"
	ViaTrial = "trial" // E2c
)

// Querier is the read side (*sql.DB, *sql.Tx and the store pools satisfy it).
//
// It is an alias of an unnamed interface so that generation.QueryReader (the same literal) is the identical type and
// *Service satisfies generation.AdoptionGate.
type Querier = interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// State is the current adoption state of one engine: the latest log row.
type State struct {
	Engine, State, Via string
	TrialID            *string
	EnvIDs             []string
	ChangedBy          string
	ChangedAt          time.Time
	Reason             *string
}

// Adopted reports State == adopted.
func (s State) Adopted() bool { return s.State == StateAdopted }

// Options builds the Service.
type Options struct {
	Store *store.Store
	Now   func() time.Time // nil => time.Now
}

// Service is the adoption record.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// State reads one engine's state; an engine without a row is StateNone, never an error.
func (s *Service) State(ctx context.Context, q Querier, engine string) (State, error) {
	rows, err := q.QueryContext(ctx, stateSQL+` WHERE engine = ?`, engine)
	if err != nil {
		return State{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return State{Engine: engine, State: StateNone}, rows.Err()
	}
	return scanState(rows)
}

// List reads the state of every engine that has a row, ordered by engine.
func (s *Service) List(ctx context.Context, q Querier) ([]State, error) {
	rows, err := q.QueryContext(ctx, stateSQL+` ORDER BY engine`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []State{}
	for rows.Next() {
		st, err := scanState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

const stateSQL = `SELECT engine, state, via, trial_id, env_ids, changed_by, changed_at, reason FROM engine_adoption`

func scanState(rows *sql.Rows) (State, error) {
	var st State
	var trial, reason sql.NullString
	var env, at string
	if err := rows.Scan(&st.Engine, &st.State, &st.Via, &trial, &env, &st.ChangedBy, &at, &reason); err != nil {
		return State{}, err
	}
	if trial.Valid {
		st.TrialID = &trial.String
	}
	if reason.Valid {
		st.Reason = &reason.String
	}
	_ = json.Unmarshal([]byte(env), &st.EnvIDs)
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return State{}, err
	}
	st.ChangedAt = t
	return st, nil
}

// Admitted implements generation.AdoptionGate: nil for yue2 and for an adopted engine, else a 409 ENGINE_NOT_ADOPTED
// problem {engine, hint}. trialID is honoured from E2c on: until trials exist, a non-empty trialID never admits.
func (s *Service) Admitted(ctx context.Context, q Querier, engine, trialID string) error {
	if engine == YuE2 {
		return nil
	}
	st, err := s.State(ctx, q, engine)
	if err != nil {
		return err
	}
	if st.Adopted() {
		return nil
	}
	return problem.E(http.StatusConflict, "ENGINE_NOT_ADOPTED", "the engine has not been adopted", map[string]any{"engine": engine, "hint": AdoptHint(engine)})
}

// Adopt appends an adopted row via 'cli' (by names the operator; reason is 1..500 characters and required). The log row and
// its audit row commit together.
func (s *Service) Adopt(ctx context.Context, by, engine, reason string) (State, error) {
	return s.append(ctx, by, engine, reason, StateAdopted, "cli:engines-adopt")
}

// Reject appends a not_adopted row via 'cli' (same rules as Adopt). History is never rewritten.
func (s *Service) Reject(ctx context.Context, by, engine, reason string) (State, error) {
	return s.append(ctx, by, engine, reason, StateNotAdopted, "cli:engines-reject")
}

func (s *Service) append(ctx context.Context, by, engine, reason, state, op string) (State, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > 500 {
		return State{}, ErrReasonRequired
	}
	by = strings.TrimSpace(by)
	if by == "" {
		return State{}, ErrByRequired
	}
	if engine == YuE2 {
		return State{}, ErrYuE2Fixed
	}
	if engine == "" || strings.ContainsAny(engine, " \t\n/") {
		return State{}, ErrBadEngine
	}
	started := s.o.Now()
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = tx.Rollback() }()
	at := store.FormatTime(started)
	if _, err := tx.ExecContext(ctx, `INSERT INTO engine_adoption_log(engine, state, via, trial_id, env_ids, changed_by, changed_at, reason)
		VALUES (?,?,?,NULL,'[]',?,?,?)`, engine, state, ViaCLI, by, at, reason); err != nil {
		return State{}, err
	}
	raw, err := json.Marshal(map[string]any{"engine": engine, "state": state, "reason": reason})
	if err != nil {
		return State{}, err
	}
	canon, err := canonjson.Canonical(raw)
	if err != nil {
		return State{}, err
	}
	sum := sha256.Sum256(canon)
	e := audit.Entry{At: started, Principal: auth.CLI, Transport: reqinfo.CLI, Op: op, Method: audit.SystemMethod, ArgsSHA256: hex.EncodeToString(sum[:]),
		Status: 200, Idempotency: audit.IdemNone, Duration: s.o.Now().Sub(started)}
	if err := audit.InsertTx(ctx, tx, e); err != nil {
		return State{}, err
	}
	st, err := s.State(ctx, tx, engine)
	if err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, err
	}
	return st, nil
}
