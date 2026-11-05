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
	"database/sql"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// ErrNotImplemented marks a seam whose body has not landed.
var ErrNotImplemented = errors.New("engineadopt: not implemented yet")

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
//
// STUB(E2-OP)
func (s *Service) State(ctx context.Context, q Querier, engine string) (State, error) {
	return State{}, ErrNotImplemented
}

// List reads the state of every engine that has a row, ordered by engine.
//
// STUB(E2-OP)
func (s *Service) List(ctx context.Context, q Querier) ([]State, error) {
	return nil, ErrNotImplemented
}

// Admitted implements generation.AdoptionGate: nil for yue2 and for an adopted engine, else a 409 ENGINE_NOT_ADOPTED
// problem {engine, hint}. trialID is honoured from E2c on.
//
// STUB(E2-OP)
func (s *Service) Admitted(ctx context.Context, q Querier, engine, trialID string) error {
	return ErrNotImplemented
}

// Adopt appends an adopted row via 'cli' (by names the operator; reason is 1..500 characters and required).
//
// STUB(E2-OP)
func (s *Service) Adopt(ctx context.Context, by, engine, reason string) (State, error) {
	return State{}, ErrNotImplemented
}

// Reject appends a not_adopted row via 'cli' (same rules as Adopt). History is never rewritten.
//
// STUB(E2-OP)
func (s *Service) Reject(ctx context.Context, by, engine, reason string) (State, error) {
	return State{}, ErrNotImplemented
}
