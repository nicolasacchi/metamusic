package api

import (
	"github.com/danielgtaylor/huma/v2"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// MandateScope is what a mandate allows (M6N C366.2). The numeric limits are checked by the service, not by the schema, so a breach
// answers 422 MANDATE_SCOPE_INVALID {field, rule} rather than a generic validation error.
type MandateScope struct {
	Modes                   []string `json:"modes" nullable:"false" doc:"Planner modes the night may use: new_seed, style_edit"`
	Engines                 []string `json:"engines" nullable:"false" doc:"Adopted engines only"`
	MinStars                int      `json:"min_stars" doc:"The parent's Owner stars, 1..5; an unrated parent never qualifies"`
	RootTrackIDs            []string `json:"root_track_ids" nullable:"true" required:"false" doc:"Lineage roots the night may work on (1..200); null means every lineage"`
	MaxTakesPerLineage      int      `json:"max_takes_per_lineage" doc:"Takes per lineage over the mandate's life, 1..50"`
	MaxTakesPerBatch        int      `json:"max_takes_per_batch" doc:"Takes per batch, 1..12"`
	MaxBatchCeilingMicroUSD int64    `json:"max_batch_ceiling_micro_usd" doc:"Largest estimate ceiling of one batch, at most the global batch cap and the budget"`
	MaxBatchesPerDay        int      `json:"max_batches_per_day" doc:"Batches per local day, 1..4"`
	TierB                   bool     `json:"tier_b" doc:"Whether the batch may carry listening checks"`
}

func scopeIn(s MandateScope) mandates.Scope {
	return mandates.Scope{Modes: s.Modes, Engines: s.Engines, MinStars: s.MinStars, RootTrackIDs: s.RootTrackIDs, MaxTakesPerLineage: s.MaxTakesPerLineage,
		MaxTakesPerBatch: s.MaxTakesPerBatch, MaxBatchCeilingMicroUSD: money.MicroUSD(s.MaxBatchCeilingMicroUSD), MaxBatchesPerDay: s.MaxBatchesPerDay, TierB: s.TierB}
}

func scopeOut(s mandates.Scope) MandateScope {
	modes, engines := s.Modes, s.Engines
	if modes == nil {
		modes = []string{}
	}
	if engines == nil {
		engines = []string{}
	}
	return MandateScope{Modes: modes, Engines: engines, MinStars: s.MinStars, RootTrackIDs: s.RootTrackIDs, MaxTakesPerLineage: s.MaxTakesPerLineage,
		MaxTakesPerBatch: s.MaxTakesPerBatch, MaxBatchCeilingMicroUSD: int64(s.MaxBatchCeilingMicroUSD), MaxBatchesPerDay: s.MaxBatchesPerDay, TierB: s.TierB}
}

// MandateRoot is the takes a mandate has used on one lineage.
type MandateRoot struct {
	RootTrackID string `json:"root_track_id"`
	Takes       int    `json:"takes"`
}

// MandateUsage is what a mandate has consumed (C367). used is settled plus reserved; committed also counts the unspent headroom of a
// running batch; excess is provider overshoot, shown and never counted.
type MandateUsage struct {
	SettledMicroUSD   int64         `json:"settled_micro_usd"`
	SettledUSD        string        `json:"settled_usd"`
	ReservedMicroUSD  int64         `json:"reserved_micro_usd"`
	ReservedUSD       string        `json:"reserved_usd"`
	UsedMicroUSD      int64         `json:"used_micro_usd"`
	UsedUSD           string        `json:"used_usd"`
	CommittedMicroUSD int64         `json:"committed_micro_usd"`
	CommittedUSD      string        `json:"committed_usd"`
	RemainingMicroUSD int64         `json:"remaining_micro_usd" doc:"Budget minus committed, never below zero"`
	RemainingUSD      string        `json:"remaining_usd"`
	ExcessMicroUSD    int64         `json:"excess_micro_usd"`
	ExcessUSD         string        `json:"excess_usd"`
	Batches           int           `json:"batches"`
	Takes             int           `json:"takes"`
	ByRoot            []MandateRoot `json:"by_root" nullable:"false"`
}

// MandateRevoked says who ended a mandate and why.
type MandateRevoked struct {
	By     PrincipalRef `json:"by"`
	At     time.Time    `json:"at"`
	Reason string       `json:"reason"`
}

// MandateRevokedOrNull is a MandateRevoked that may be null (the repo's OrNull convention: Huma refuses nullable on an object $ref).
type MandateRevokedOrNull MandateRevoked

type mandateRevokedShape MandateRevoked

// Schema implements huma.SchemaProvider.
func (MandateRevokedOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[mandateRevokedShape](r)
}

// Mandate is a standing spend approval the Owner created in the app (C369.2).
type Mandate struct {
	ID             string                `json:"id"`
	Holder         string                `json:"holder" enum:"night_shift"`
	Title          string                `json:"title"`
	BudgetMicroUSD int64                 `json:"budget_micro_usd"`
	BudgetUSD      string                `json:"budget_usd"`
	ValidFrom      time.Time             `json:"valid_from"`
	ValidUntil     time.Time             `json:"valid_until"`
	EffectiveState string                `json:"effective_state" enum:"scheduled,live,ended,revoked"`
	Scope          MandateScope          `json:"scope"`
	Usage          MandateUsage          `json:"usage"`
	CreatedBy      PrincipalRef          `json:"created_by"`
	CreatedAt      time.Time             `json:"created_at"`
	Revoked        *MandateRevokedOrNull `json:"revoked" doc:"Set once the mandate is revoked"`
}

// MandatePage is one page of list_mandates, newest first.
type MandatePage struct {
	Items      []Mandate `json:"items" nullable:"false"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type createMandateIn struct {
	IdemHeader
	Body struct {
		Holder         string       `json:"holder" enum:"night_shift" doc:"Who may spend under the mandate; the night shift is the only holder"`
		Title          string       `json:"title" doc:"1..120 characters"`
		BudgetMicroUSD int64        `json:"budget_micro_usd" doc:"1..100 000 000 micro-USD, counted against settled plus reserved spend"`
		ValidFrom      *time.Time   `json:"valid_from,omitempty" doc:"Default now; at most five minutes in the past"`
		ValidUntil     time.Time    `json:"valid_until" doc:"After valid_from and at most 31 days later"`
		Scope          MandateScope `json:"scope"`
	}
}

type mandateOut struct {
	Status int
	Body   Mandate
}

type listMandatesIn struct {
	State          string   `query:"state" enum:"approved,revoked" doc:"The stored state"`
	EffectiveState []string `query:"effective_state,explode" nullable:"false" enum:"scheduled,live,ended,revoked"`
	Holder         string   `query:"holder" enum:"night_shift"`
	Limit          int      `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor         string   `query:"cursor"`
}

type mandatePageOut struct{ Body MandatePage }

type getMandateIn struct {
	ID string `path:"id" doc:"Mandate id"`
}

type revokeMandateIn struct {
	ID   string `path:"id" doc:"Mandate id"`
	Body struct {
		Reason string `json:"reason" doc:"Why, 1..500 characters"`
	}
}

func usageOut(budget money.MicroUSD, u spend.MandateUsage) MandateUsage {
	roots := make([]MandateRoot, 0, len(u.ByRoot))
	for id, n := range u.ByRoot {
		roots = append(roots, MandateRoot{RootTrackID: id, Takes: n})
	}
	sortRoots(roots)
	remaining := max(0, budget-u.Committed)
	return MandateUsage{
		SettledMicroUSD: int64(u.Settled), SettledUSD: u.Settled.USD(), ReservedMicroUSD: int64(u.Reserved), ReservedUSD: u.Reserved.USD(),
		UsedMicroUSD: int64(u.Used), UsedUSD: u.Used.USD(), CommittedMicroUSD: int64(u.Committed), CommittedUSD: u.Committed.USD(),
		RemainingMicroUSD: int64(remaining), RemainingUSD: remaining.USD(), ExcessMicroUSD: int64(u.Excess), ExcessUSD: u.Excess.USD(),
		Batches: u.Batches, Takes: u.Takes, ByRoot: roots,
	}
}

func sortRoots(r []MandateRoot) {
	sort.Slice(r, func(i, j int) bool {
		if r[i].Takes != r[j].Takes {
			return r[i].Takes > r[j].Takes
		}
		return r[i].RootTrackID < r[j].RootTrackID
	})
}

// mandateRef renders a stored principal key ("human:alice", "device:d1", "agent:t", "system:x") for the viewer.
func mandateRef(viewer auth.Principal, key string) PrincipalRef {
	kind, name, _ := strings.Cut(key, ":")
	return RefFor(viewer, key, kind, name)
}
