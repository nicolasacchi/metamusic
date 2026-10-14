// Package generation turns estimate items into takes (lineage + engine + lint), freezes an estimate and
// runs the create, approve and reject transactions (M3 C71, C72.4, C72.6). It imports neither jobs nor
// api: the runner is woken through the injected Wake func and the response body is encoded by a callback
// the operation supplies (TestImportGraph_M3). It imports no engine implementation either: requests are
// parsed, patched and linted through lineage, and the engine is used through the engine.Engine interface.
package generation

import (
	"io"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

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
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	return &Service{o: o}
}

// OptString is a tri-state patch string: absent, JSON null (remove) or a value (set).
type OptString = lineage.OptString

// OptNumber is the numeric twin of OptString.
type OptNumber = lineage.OptNumber

// Patch edits a base recipe (C71.1); nil pointers keep the base value. It is lineage's type: the planner
// applies it, and generation never imports the engine implementation.
type Patch = lineage.Patch

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

// Pot is one cap with its use (month and day of an estimate's budget block).
type Pot struct{ Cap, Used, Left money.MicroUSD }

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
	GPUPools         []string
	Items            []PlannedItem
	Warnings         []Warning
	RequiresApproval bool
	ApprovalReasons  []string
	Budget           CapFit
	Month, Day       Pot
	BatchCap         money.MicroUSD
	Provider         provider.State
}

// CreateIn is a create request (C72.4). VariantTrackID is set by create_variant.
type CreateIn struct {
	Token, Rationale, Via string
	VariantTrackID        *string
}

// Created is a batch as the create, approve and reject operations answer it: what the create transaction
// wrote, read back inside that transaction (so the stored idempotent body and the live answer are one
// value), or what an approval changed.
type Created struct {
	BatchID, State, ApprovalState string
	JobIDs                        []string
	Takes                         int
	Expected, Max, Ceiling        money.MicroUSD
	CreatedAt                     time.Time

	Via, EstimateID string
	Rationale       *string
	FinishedAt      *time.Time
	CreatedBy       Actor
	Counts          Counts
	Reserved        money.MicroUSD
	Settled         money.MicroUSD
	Outstanding     money.MicroUSD
	Reconciled      bool
	Approval        ApprovalView
	Jobs            []JobView
}

// Actor is a principal reference as stored (Principal.Key plus the kind and display name).
type Actor struct{ Key, Kind, Name string }

// Counts counts a batch's jobs by state class (C81 BatchCounts).
type Counts struct{ Awaiting, Queued, Running, Completed, Truncated, Failed, Cancelled, Expired int }

// JobView is one job of a batch (C81 JobSummary).
type JobView struct {
	ID, BatchID, State, Title, RequestID, Operator string
	TakeIndex                                      int
	Seed                                           int64
	Outcome, ErrorCode                             *string
	ParentTrackID, DraftID, TrackID                *string
	CreatedAt, UpdatedAt                           time.Time
	Expected                                       money.MicroUSD
	Display                                        *money.MicroUSD
	Basis                                          string
}

// ApprovalView is the approval row of a batch (C81 ApprovalStatus).
type ApprovalView struct {
	BatchID, State, Via, Reason, DecidedBy string
	Decider                                Actor // DecidedBy as a reference; zero while pending
	Reasons                                []string
	DecidedAt                              *time.Time
	ExpiresAt                              time.Time
	LastRequestedAt, NextRequestAllowedAt  *time.Time
	Notified                               bool
}
