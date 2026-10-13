// Package generation turns estimate items into takes (lineage + engine + lint), freezes an estimate and
// runs the create, approve and reject transactions (M3 C71, C72.4, C72.6). It imports neither jobs nor
// api: the runner is woken through the injected Wake func and the response body is encoded by a callback
// the operation supplies (TestImportGraph_M3). M3-B2 freezes the constructor and the method set; M3-B4
// replaces the STUB bodies.
package generation

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ErrNotImplemented marks a STUB(M3-B4) body.
var ErrNotImplemented = errors.New("generation: not implemented (STUB(M3-B4))")

// Options builds a Service.
type Options struct {
	Store     *store.Store
	Library   *library.Service
	Lineage   *lineage.Service
	Spend     *spend.Service
	Engines   engine.Registry
	Gate      *provider.Gate // generation-enabled state from local facts (C68); never the network
	Notify    notify.Notifier
	PublicURL string
	Now       func() time.Time // nil => time.Now
	NewID     func() string    // nil => UUIDv7
	Rand      io.Reader        // nil => crypto/rand
	// Wake nudges the runner after a commit; nil-safe (nil in mcp-stdio, where no runner runs).
	Wake func()
	// Cfg values frozen into quotes (C72.1).
	ColdStartS, KillSlackS int
	MaxRuntime             time.Duration
	PriceMarginPPM         int64
}

// Service owns the create path of generation.
type Service struct {
	o Options
}

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// OptString is a tri-state patch string: absent, JSON null (remove) or a value (set).
type OptString struct {
	Set, Null bool
	Value     string
}

// OptNumber is the numeric twin of OptString.
type OptNumber struct {
	Set, Null bool
	Value     float64
}

// Patch edits a base recipe (C71.1); nil pointers keep the base value.
type Patch struct {
	Style, Lyrics, Cot *string
	ABC                OptString
	CFGScale           OptNumber
}

// Item is the one input shape behind every create path (C71.1). Recipe holds the exact client bytes.
type Item struct {
	Mode           string
	ParentTrackID  *string
	ParentTrackIDs []string
	DraftIDs       []string
	Recipe         []byte
	Patch          *Patch
	TemplateMode   string
	NSeeds         int
	Seeds          []int64
	Title          *string
}

// EstimateIn is an estimate request; Engine "" means the default engine.
type EstimateIn struct {
	Engine string
	Items  []Item
	// Via is "api" or "human_create" (the path the later create will use, C72.6).
	Via string
}

// Warning is one estimate warning for a take.
type Warning struct {
	TakeIndex                   int
	Code, Field, Severity, Text string
}

// CostBlock is the money of an estimate (C72.1).
type CostBlock struct {
	Expected, Max, RetryAllowance, Ceiling, AttemptOverhead money.MicroUSD
}

// TakePlan is one planned take (C71.4).
type TakePlan struct {
	TakeIndex      int
	RequestID      string
	Seed           int64
	Title          string
	Operator       string
	ParentTrackID  *string
	DraftID        *string
	ExpectedAudioS int
	TimeoutS       int
	Max            money.MicroUSD
}

// PlannedItem groups the takes one item expanded to.
type PlannedItem struct {
	Index int
	Mode  string
	Takes []TakePlan
}

// CapFit says which caps a create would hit now.
type CapFit struct {
	Fits        bool
	WouldExceed []string // "batch" | "monthly" | "daily"
}

// Estimate is a frozen estimate with its single-use confirmation token (shown once).
type Estimate struct {
	ID, Token        string
	ExpiresAt        time.Time
	Engine, Backend  string
	Takes            int
	Cost             CostBlock
	ETAS             int
	PricePerHour     money.MicroUSD
	PriceObservedAt  *time.Time
	Items            []PlannedItem
	Warnings         []Warning
	RequiresApproval bool
	ApprovalReasons  []string
	Budget           CapFit
	Provider         provider.State
}

// Estimate expands items into takes, costs them and stores the frozen estimate. STUB(M3-B4).
func (s *Service) Estimate(ctx context.Context, p auth.Principal, in EstimateIn) (Estimate, error) {
	return Estimate{}, ErrNotImplemented
}

// CreateIn is a create request (C72.4). VariantTrackID is set by create_variant.
type CreateIn struct {
	Token, Rationale, Via string
	VariantTrackID        *string
}

// Created is what a create transaction wrote.
type Created struct {
	BatchID, State, ApprovalState string
	JobIDs                        []string
	Takes                         int
	Expected, Max, Ceiling        money.MicroUSD
	CreatedAt                     time.Time
}

// Create runs the create transaction. encode renders the response body inside the transaction (so the
// idempotency row stores the exact bytes) and is supplied by the operation. STUB(M3-B4).
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateIn, encode func(Created) (status int, body []byte, err error)) (Created, error) {
	return Created{}, ErrNotImplemented
}

// ApprovalView is the approval row of a batch (C81 ApprovalStatus).
type ApprovalView struct {
	BatchID, State, Via, Reason, DecidedBy string
	Reasons                                []string
	DecidedAt                              *time.Time
	ExpiresAt                              time.Time
	LastRequestedAt, NextRequestAllowedAt  *time.Time
	Notified                               bool
}

// Approval reads the approval of a batch. STUB(M3-B4).
func (s *Service) Approval(ctx context.Context, p auth.Principal, batchID string) (ApprovalView, error) {
	return ApprovalView{}, ErrNotImplemented
}

// RequestApproval pushes the approval request for a pending batch (creator only; rate-limited to one
// push per 10 minutes). STUB(M3-B4).
func (s *Service) RequestApproval(ctx context.Context, p auth.Principal, batchID string) (ApprovalView, error) {
	return ApprovalView{}, ErrNotImplemented
}

// Approve approves a pending batch (human-only) and wakes the runner. STUB(M3-B4).
func (s *Service) Approve(ctx context.Context, p auth.Principal, batchID, reason string) (Created, error) {
	return Created{}, ErrNotImplemented
}

// Reject rejects a pending batch, releasing its reservation in the same transaction. STUB(M3-B4).
func (s *Service) Reject(ctx context.Context, p auth.Principal, batchID, reason string) (Created, error) {
	return Created{}, ErrNotImplemented
}
