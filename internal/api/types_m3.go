package api

import (
	"encoding/json"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/library"
)

// Types of the M3 operations (contract C81). Money is integer micro-USD everywhere: every amount is a
// `<x>_micro_usd` integer plus the exact decimal `<x>_usd` string (D-M3-6). Principals are PrincipalRef
// rendered through RefFor. M3-B2 freezes these shapes; openapi.json and schema.d.ts change no more.

// Body limits (bytes) of the M3 operations (C81).
const (
	bodyEstimateGeneration = 2 << 20
	bodyEstimateVariant    = 1 << 20
	bodyConfirm            = bodySmall
)

// ---- tri-state patch values (C71.1) ----

// NullableString distinguishes absent, JSON null and a value: absent keeps the base value, null removes
// it, a string sets it.
type NullableString struct {
	Set, Null bool
	Value     string
}

// Schema implements huma.SchemaProvider: `string | null`, never Huma's empty schema.
func (NullableString) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Nullable: true}
}

// UnmarshalJSON records presence (Go only calls it when the key is present).
func (n *NullableString) UnmarshalJSON(b []byte) error {
	*n = NullableString{Set: true}
	if string(b) == "null" {
		n.Null = true
		return nil
	}
	return json.Unmarshal(b, &n.Value)
}

// MarshalJSON writes null, the string or (for the unset zero value) null.
func (n NullableString) MarshalJSON() ([]byte, error) {
	if !n.Set || n.Null {
		return []byte("null"), nil
	}
	return json.Marshal(n.Value)
}

// IsZero lets `omitzero` drop an unset value.
func (n NullableString) IsZero() bool { return !n.Set }

// NullableNumber is the numeric twin of NullableString.
type NullableNumber struct {
	Set, Null bool
	Value     float64
}

// Schema implements huma.SchemaProvider: `number | null`.
func (NullableNumber) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeNumber, Nullable: true}
}

// UnmarshalJSON records presence.
func (n *NullableNumber) UnmarshalJSON(b []byte) error {
	*n = NullableNumber{Set: true}
	if string(b) == "null" {
		n.Null = true
		return nil
	}
	return json.Unmarshal(b, &n.Value)
}

// MarshalJSON writes null, the number or (unset) null.
func (n NullableNumber) MarshalJSON() ([]byte, error) {
	if !n.Set || n.Null {
		return []byte("null"), nil
	}
	return json.Marshal(n.Value)
}

// IsZero lets `omitzero` drop an unset value.
func (n NullableNumber) IsZero() bool { return !n.Set }

// nullableSchema renders S (a method-less shadow of the real type) as an inline object schema with
// Nullable=true: Huma refuses nullable on an object $ref (repo convention, see library.nullableObject).
func nullableSchema[S any](r huma.Registry) *huma.Schema {
	cp := *huma.SchemaFromType(r, reflect.TypeFor[S]())
	cp.Nullable = true
	return &cp
}

// ---- items (C71.1) ----

// Patch edits a base recipe; absent keys keep the base value.
type Patch struct {
	Style    *string        `json:"style,omitempty" minLength:"1" maxLength:"4000"`
	Lyrics   *string        `json:"lyrics,omitempty" minLength:"1" maxLength:"20000"`
	Cot      *string        `json:"cot,omitempty" enum:"full,melody,off"`
	ABC      NullableString `json:"abc,omitzero" doc:"A string sets the score, null removes it, absent keeps it"`
	CFGScale NullableNumber `json:"cfg_scale,omitzero" doc:"A number sets it, null removes it, absent keeps it"`
}

// ItemIn is the one input shape behind every create path; estimate_variant builds one item.
type ItemIn struct {
	Mode           string     `json:"mode" enum:"new_seed,same_seed_edit,lyric_edit,style_edit,from_draft,compose_new,from_template" doc:"How the take list is built; see the modes table"`
	ParentTrackID  *string    `json:"parent_track_id,omitempty" doc:"Variants: the mother (required). from_draft: optional mother override"`
	ParentTrackIDs []string   `json:"parent_track_ids,omitempty" nullable:"false" minItems:"2" maxItems:"50" doc:"from_template: the mothers"`
	DraftIDs       []string   `json:"draft_ids,omitempty" nullable:"false" minItems:"1" maxItems:"50" doc:"from_draft: the drafts to render"`
	Recipe         RawRequest `json:"recipe,omitempty" doc:"compose_new: the song request, as sent"`
	Patch          *Patch     `json:"patch,omitempty" doc:"*_edit modes and from_template"`
	TemplateMode   string     `json:"template_mode,omitempty" enum:"new_seed,same_seed_edit,lyric_edit,style_edit" doc:"from_template: the mode applied to every mother"`
	NSeeds         int        `json:"n_seeds,omitempty" minimum:"1" maximum:"10" doc:"Seeds per base recipe (default 1)"`
	Seeds          []int64    `json:"seeds,omitempty" nullable:"false" minItems:"1" maxItems:"10" doc:"Explicit seeds (unique, 0..2^53-1); override n_seeds"`
	Title          *string    `json:"title,omitempty" minLength:"1" maxLength:"200" doc:"compose_new, single-take items"`
}

// ---- money building blocks ----

// Pot is one cap with its use.
type Pot struct {
	CapMicroUSD  int64  `json:"cap_micro_usd"`
	CapUSD       string `json:"cap_usd"`
	UsedMicroUSD int64  `json:"used_micro_usd"`
	UsedUSD      string `json:"used_usd"`
	LeftMicroUSD int64  `json:"left_micro_usd"`
	LeftUSD      string `json:"left_usd"`
}

// ProviderBrief is the provider block of estimates and the budget.
type ProviderBrief struct {
	GenerationEnabled bool    `json:"generation_enabled"`
	DisabledReason    *string `json:"disabled_reason" doc:"backend_none, keys_absent, endpoint_inactive, endpoint_config, quote_stale, catalog_unreadable or sweep_unreadable"`
	Armed             bool    `json:"armed"`
	Tripped           bool    `json:"tripped"`
}

// Balance is the provider account balance when readable.
type Balance struct {
	MicroUSD *int64  `json:"micro_usd"`
	USD      *string `json:"usd"`
	Source   string  `json:"source" enum:"graphql,unknown"`
}

// ---- estimates ----

// EstimateCost is the cost block of an estimate (C72.1, D-M3-29).
type EstimateCost struct {
	ExpectedMicroUSD        int64  `json:"expected_micro_usd"`
	ExpectedUSD             string `json:"expected_usd"`
	MaxMicroUSD             int64  `json:"max_micro_usd" doc:"The no-retry worst case: every take's reserve plus one attempt overhead per take"`
	MaxUSD                  string `json:"max_usd"`
	RetryAllowanceMicroUSD  int64  `json:"retry_allowance_micro_usd"`
	RetryAllowanceUSD       string `json:"retry_allowance_usd"`
	CeilingMicroUSD         int64  `json:"ceiling_micro_usd" doc:"What the batch may ever reserve: max plus the retry allowance"`
	CeilingUSD              string `json:"ceiling_usd"`
	CeilingDisplay          string `json:"ceiling_display" example:"$0.70" doc:"The ceiling rounded up for display (up to ...)"`
	AttemptOverheadMicroUSD int64  `json:"attempt_overhead_micro_usd"`
	AttemptOverheadUSD      string `json:"attempt_overhead_usd"`
}

// EstimatePrice is the price the quote used.
type EstimatePrice struct {
	PerHourMicroUSD int64    `json:"per_hour_micro_usd"`
	PerHourUSD      string   `json:"per_hour_usd"`
	GPUPools        []string `json:"gpu_pools" nullable:"false"`
	ObservedAt      *string  `json:"observed_at" doc:"RFC 3339 UTC; null for the fake backend"`
}

// TakePlan is one planned take.
type TakePlan struct {
	TakeIndex      int     `json:"take_index"`
	RequestID      string  `json:"request_id"`
	Seed           int64   `json:"seed"`
	Title          string  `json:"title"`
	Operator       string  `json:"operator"`
	ParentTrackID  *string `json:"parent_track_id"`
	DraftID        *string `json:"draft_id"`
	ExpectedAudioS int     `json:"expected_audio_s"`
	TimeoutS       int     `json:"timeout_s"`
	MaxMicroUSD    int64   `json:"max_micro_usd"`
	MaxUSD         string  `json:"max_usd"`
}

// EstimateItem groups the takes one input item expanded to.
type EstimateItem struct {
	Index int        `json:"index"`
	Mode  string     `json:"mode"`
	Takes []TakePlan `json:"takes" nullable:"false"`
}

// EstimateWarning is a lint or planner warning for one take.
type EstimateWarning struct {
	TakeIndex int    `json:"take_index"`
	Code      string `json:"code"`
	Field     string `json:"field"`
	Severity  string `json:"severity" enum:"warning,error"`
	Message   string `json:"message"`
}

// EstimateBudget says which caps a create would hit right now.
type EstimateBudget struct {
	Fits             bool     `json:"fits"`
	WouldExceed      []string `json:"would_exceed" nullable:"false" doc:"batch, monthly and/or daily"`
	Month            Pot      `json:"month"`
	Day              Pot      `json:"day"`
	BatchCapMicroUSD int64    `json:"batch_cap_micro_usd"`
	BatchCapUSD      string   `json:"batch_cap_usd"`
}

// Estimate is a frozen plan with its single-use confirmation token.
type Estimate struct {
	EstimateID            string            `json:"estimate_id"`
	ConfirmationToken     string            `json:"confirmation_token" doc:"mmc_..., single use, 10 minutes, bound to this principal; shown only here"`
	ExpiresAt             string            `json:"expires_at" doc:"RFC 3339 UTC"`
	Engine                string            `json:"engine"`
	Backend               string            `json:"backend"`
	Takes                 int               `json:"takes"`
	Cost                  EstimateCost      `json:"cost"`
	ETAS                  int               `json:"eta_s"`
	Price                 EstimatePrice     `json:"price"`
	Items                 []EstimateItem    `json:"items" nullable:"false"`
	Warnings              []EstimateWarning `json:"warnings" nullable:"false"`
	RequiresHumanApproval bool              `json:"requires_human_approval"`
	ApprovalReasons       []string          `json:"approval_reasons" nullable:"false" doc:"human_api_path, takes_over_limit or cost_over_limit"`
	Budget                EstimateBudget    `json:"budget"`
	Provider              ProviderBrief     `json:"provider"`
}

// ---- approvals, batches, jobs ----

// PrincipalRefOrNull is a PrincipalRef that may be null.
type PrincipalRefOrNull PrincipalRef

type principalRefShape PrincipalRef

// Schema implements huma.SchemaProvider.
func (PrincipalRefOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[principalRefShape](r)
}

// ApprovalStatus is the approval of a batch.
type ApprovalStatus struct {
	BatchID              string              `json:"batch_id"`
	State                string              `json:"state" enum:"pending,approved,rejected,expired,auto"`
	Reasons              []string            `json:"reasons" nullable:"false"`
	Via                  *string             `json:"via" doc:"auto, human_create, web or expiry; null while pending"`
	DecidedBy            *PrincipalRefOrNull `json:"decided_by"`
	DecidedAt            *string             `json:"decided_at"`
	Reason               *string             `json:"reason"`
	ExpiresAt            string              `json:"expires_at"`
	LastRequestedAt      *string             `json:"last_requested_at"`
	NextRequestAllowedAt *string             `json:"next_request_allowed_at"`
	Notified             *bool               `json:"notified" doc:"request_approval only: whether a push was sent now"`
}

// BatchCounts counts a batch's jobs by state class.
type BatchCounts struct {
	Awaiting  int `json:"awaiting"`
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Truncated int `json:"truncated"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
	Expired   int `json:"expired"`
}

// BatchCost is the money of a batch.
type BatchCost struct {
	ExpectedMicroUSD    int64  `json:"expected_micro_usd"`
	ExpectedUSD         string `json:"expected_usd"`
	MaxMicroUSD         int64  `json:"max_micro_usd"`
	MaxUSD              string `json:"max_usd"`
	CeilingMicroUSD     int64  `json:"ceiling_micro_usd"`
	CeilingUSD          string `json:"ceiling_usd"`
	ReservedMicroUSD    int64  `json:"reserved_micro_usd"`
	ReservedUSD         string `json:"reserved_usd"`
	SettledMicroUSD     int64  `json:"settled_micro_usd"`
	SettledUSD          string `json:"settled_usd"`
	OutstandingMicroUSD int64  `json:"outstanding_micro_usd"`
	OutstandingUSD      string `json:"outstanding_usd"`
	Reconciled          bool   `json:"reconciled" doc:"Billing has been matched and the residual released"`
}

// JobCost is the cost shown for one job.
type JobCost struct {
	ExpectedMicroUSD int64   `json:"expected_micro_usd"`
	ExpectedUSD      string  `json:"expected_usd"`
	DisplayMicroUSD  *int64  `json:"display_micro_usd"`
	DisplayUSD       *string `json:"display_usd"`
	Basis            string  `json:"basis" enum:"estimate,provisional,reconciled"`
}

// JobSummary is one job in lists and batches.
type JobSummary struct {
	ID            string  `json:"id"`
	BatchID       string  `json:"batch_id"`
	TakeIndex     int     `json:"take_index"`
	State         string  `json:"state" enum:"awaiting_confirmation,queued,submitting,starting,generating,transferring,ingesting,cancelling,failing,completed,failed,cancelled,expired"`
	Outcome       *string `json:"outcome" enum:"clean,truncated" doc:"Completed jobs only"`
	ErrorCode     *string `json:"error_code" doc:"Failed, cancelled and expired jobs; see the job error codes"`
	Title         string  `json:"title"`
	RequestID     string  `json:"request_id"`
	Seed          int64   `json:"seed"`
	Operator      string  `json:"operator"`
	ParentTrackID *string `json:"parent_track_id"`
	DraftID       *string `json:"draft_id"`
	TrackID       *string `json:"track_id"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	Cost          JobCost `json:"cost"`
}

// JobTruncated says which streams hit the token limit.
type JobTruncated struct {
	ABC      bool `json:"abc"`
	Semantic bool `json:"semantic"`
}

// JobTruncatedOrNull may be null.
type JobTruncatedOrNull JobTruncated

type jobTruncatedShape JobTruncated

// Schema implements huma.SchemaProvider.
func (JobTruncatedOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[jobTruncatedShape](r)
}

// JobArtifact is one uploaded file of the job.
type JobArtifact struct {
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	Verified bool   `json:"verified"`
}

// JobTimings are measured durations.
type JobTimings struct {
	QueueDelayMS *int64 `json:"queue_delay_ms"`
	ExecutionMS  *int64 `json:"execution_ms"`
	ColdStartS   *int64 `json:"cold_start_s" doc:"Announced time minus the worker's start"`
}

// JobEvent is one entry of the job's append-only event log.
type JobEvent struct {
	At    string  `json:"at"`
	From  *string `json:"from"`
	To    *string `json:"to"`
	Event string  `json:"event"`
	Code  *string `json:"code"`
}

// Job is the full view of one job. Provider refs, upload URLs and the endpoint id never appear.
type Job struct {
	JobSummary
	Engine            string              `json:"engine"`
	Backend           string              `json:"backend"`
	Mode              string              `json:"mode"`
	Attempts          int                 `json:"attempts"`
	Resubmits         int                 `json:"resubmits"`
	StartedAt         *string             `json:"started_at"`
	FinishedAt        *string             `json:"finished_at"`
	DeadlineAt        *string             `json:"deadline_at"`
	TimeoutS          int                 `json:"timeout_s"`
	ExpectedAudioS    int                 `json:"expected_audio_s"`
	AudioS            *float64            `json:"audio_s"`
	Truncated         *JobTruncatedOrNull `json:"truncated"`
	Env               *library.Env        `json:"env"`
	Artifacts         []JobArtifact       `json:"artifacts" nullable:"false"`
	CancelRequestedAt *string             `json:"cancel_requested_at"`
	Timings           JobTimings          `json:"timings"`
	ErrorMessage      *string             `json:"error_message" doc:"A fixed English template per code, never provider text"`
	Events            []JobEvent          `json:"events" nullable:"false" doc:"Newest 50"`
}

// Batch is a batch with its jobs.
type Batch struct {
	BatchSummary
	Jobs []JobSummary `json:"jobs" nullable:"false" doc:"At most 50, in take order"`
}

// BatchSummary is a batch without its jobs.
type BatchSummary struct {
	ID         string         `json:"id"`
	State      string         `json:"state" enum:"awaiting_confirmation,active,finished,rejected,expired"`
	Via        string         `json:"via" enum:"api,human_create"`
	CreatedBy  PrincipalRef   `json:"created_by"`
	CreatedAt  string         `json:"created_at"`
	FinishedAt *string        `json:"finished_at"`
	Rationale  *string        `json:"rationale"`
	Takes      int            `json:"takes"`
	Counts     BatchCounts    `json:"counts"`
	Cost       BatchCost      `json:"cost"`
	Approval   ApprovalStatus `json:"approval"`
	EstimateID string         `json:"estimate_id"`
}

// BatchPage is one page of batches.
type BatchPage struct {
	Items      []BatchSummary `json:"items" nullable:"false"`
	NextCursor *string        `json:"next_cursor"`
}

// JobPage is one page of jobs.
type JobPage struct {
	Items      []JobSummary `json:"items" nullable:"false"`
	NextCursor *string      `json:"next_cursor"`
}

// WaitJobResult is the answer of wait_for_job.
type WaitJobResult struct {
	Job      Job  `json:"job"`
	Changed  bool `json:"changed" doc:"The state differs from since_state (or the job is terminal)"`
	TimedOut bool `json:"timed_out"`
}

// ---- budget, spend, caps ----

// CapToken is one agent token's limits.
type CapToken struct {
	TokenID             string  `json:"token_id"`
	Name                string  `json:"name"`
	DailyBudgetMicroUSD *int64  `json:"daily_budget_micro_usd" doc:"null: the global agent daily cap applies"`
	DailyBudgetUSD      *string `json:"daily_budget_usd"`
	AutoApproveTakes    *int    `json:"auto_approve_takes"`
	AutoApproveMicroUSD *int64  `json:"auto_approve_micro_usd"`
	AutoApproveUSD      *string `json:"auto_approve_usd"`
}

// CapsView is the cap configuration. Human callers see every token, agent callers their own.
type CapsView struct {
	MonthlyMicroUSD     int64        `json:"monthly_micro_usd"`
	MonthlyUSD          string       `json:"monthly_usd"`
	BatchMicroUSD       int64        `json:"batch_micro_usd"`
	BatchUSD            string       `json:"batch_usd"`
	HumanDailyMicroUSD  int64        `json:"human_daily_micro_usd"`
	HumanDailyUSD       string       `json:"human_daily_usd"`
	AgentDailyMicroUSD  int64        `json:"agent_daily_micro_usd"`
	AgentDailyUSD       string       `json:"agent_daily_usd"`
	AutoApproveTakes    int          `json:"auto_approve_takes"`
	AutoApproveMicroUSD int64        `json:"auto_approve_micro_usd"`
	AutoApproveUSD      string       `json:"auto_approve_usd"`
	UpdatedBy           PrincipalRef `json:"updated_by"`
	UpdatedAt           string       `json:"updated_at"`
	Tokens              []CapToken   `json:"tokens" nullable:"false"`
	// Connectors are the per-grant limits of hosted connectors (M4a C106): a human sees all, an OAuth caller its own grant, a PAT caller none.
	Connectors []CapConnector `json:"connectors" nullable:"false"`
}

// BudgetMonth is the current calendar month.
type BudgetMonth struct {
	Start                   string `json:"start" doc:"RFC 3339, start of the month in the server's time zone"`
	SettledMicroUSD         int64  `json:"settled_micro_usd"`
	SettledUSD              string `json:"settled_usd"`
	BillAdjustmentsMicroUSD int64  `json:"bill_adjustments_micro_usd"`
	BillAdjustmentsUSD      string `json:"bill_adjustments_usd"`
	OutstandingMicroUSD     int64  `json:"outstanding_micro_usd"`
	OutstandingUSD          string `json:"outstanding_usd"`
	CapMicroUSD             int64  `json:"cap_micro_usd"`
	CapUSD                  string `json:"cap_usd"`
	LeftMicroUSD            int64  `json:"left_micro_usd"`
	LeftUSD                 string `json:"left_usd"`
}

// BudgetDay is today's use by the caller.
type BudgetDay struct {
	Principal           PrincipalRef `json:"principal"`
	SettledMicroUSD     int64        `json:"settled_micro_usd"`
	SettledUSD          string       `json:"settled_usd"`
	OutstandingMicroUSD int64        `json:"outstanding_micro_usd"`
	OutstandingUSD      string       `json:"outstanding_usd"`
	CapMicroUSD         int64        `json:"cap_micro_usd"`
	CapUSD              string       `json:"cap_usd"`
	LeftMicroUSD        int64        `json:"left_micro_usd"`
	LeftUSD             string       `json:"left_usd"`
}

// AutoApprove is the caller's effective auto-approve limits.
type AutoApprove struct {
	Takes    int    `json:"takes"`
	MicroUSD int64  `json:"micro_usd"`
	USD      string `json:"usd"`
}

// Budget is what the caller may still spend.
type Budget struct {
	Caps        CapsView      `json:"caps"`
	Month       BudgetMonth   `json:"month"`
	Day         BudgetDay     `json:"day"`
	AutoApprove AutoApprove   `json:"auto_approve"`
	Balance     Balance       `json:"balance"`
	Provider    ProviderBrief `json:"provider"`
}

// SpendDay is one day's settled spend.
type SpendDay struct {
	Date            string `json:"date" example:"2026-10-14"`
	SettledMicroUSD int64  `json:"settled_micro_usd"`
	SettledUSD      string `json:"settled_usd"`
}

// SpendBatch is one batch of the month.
type SpendBatch struct {
	ID               string       `json:"id"`
	CreatedAt        string       `json:"created_at"`
	CreatedBy        PrincipalRef `json:"created_by"`
	Takes            int          `json:"takes"`
	State            string       `json:"state"`
	ExpectedMicroUSD int64        `json:"expected_micro_usd"`
	ExpectedUSD      string       `json:"expected_usd"`
	MaxMicroUSD      int64        `json:"max_micro_usd"`
	MaxUSD           string       `json:"max_usd"`
	SettledMicroUSD  int64        `json:"settled_micro_usd"`
	SettledUSD       string       `json:"settled_usd"`
	Reconciled       bool         `json:"reconciled"`
}

// SpendTotals are the month's totals.
type SpendTotals struct {
	SettledMicroUSD         int64  `json:"settled_micro_usd"`
	SettledUSD              string `json:"settled_usd"`
	BillAdjustmentsMicroUSD int64  `json:"bill_adjustments_micro_usd"`
	BillAdjustmentsUSD      string `json:"bill_adjustments_usd"`
	OutstandingMicroUSD     int64  `json:"outstanding_micro_usd"`
	OutstandingUSD          string `json:"outstanding_usd"`
}

// Spend is the month's spend report.
type Spend struct {
	Month   string       `json:"month" example:"2026-10"`
	Days    []SpendDay   `json:"days" nullable:"false"`
	Batches []SpendBatch `json:"batches" nullable:"false"`
	Totals  SpendTotals  `json:"totals"`
}

// LedgerEntry is one row of the append-only ledger.
type LedgerEntry struct {
	ID        int64        `json:"id"`
	At        string       `json:"at"`
	Kind      string       `json:"kind" enum:"reserve,settle,residual,release,bill"`
	MicroUSD  int64        `json:"micro_usd"`
	USD       string       `json:"usd"`
	BatchID   *string      `json:"batch_id"`
	JobID     *string      `json:"job_id"`
	Reason    string       `json:"reason"`
	Principal PrincipalRef `json:"principal"`
}

// LedgerPage is one page of ledger entries.
type LedgerPage struct {
	Items      []LedgerEntry `json:"items" nullable:"false"`
	NextCursor *string       `json:"next_cursor"`
}

// ---- provider status ----

// TripView is one provider trip.
type TripView struct {
	ID        string   `json:"id"`
	TrippedAt string   `json:"tripped_at"`
	Reason    string   `json:"reason"`
	Actions   []string `json:"actions" nullable:"false" doc:"What the reconciler did"`
	ResetAt   *string  `json:"reset_at"`
}

// TripOrNull may be null.
type TripOrNull TripView

type tripShape TripView

// Schema implements huma.SchemaProvider.
func (TripOrNull) Schema(r huma.Registry) *huma.Schema { return nullableSchema[tripShape](r) }

// WorkerCounts is the last worker sweep.
type WorkerCounts struct {
	Running      int    `json:"running"`
	Idle         int    `json:"idle"`
	Initializing int    `json:"initializing"`
	Throttled    int    `json:"throttled"`
	Unhealthy    int    `json:"unhealthy"`
	ObservedAt   string `json:"observed_at"`
	// InitializingSince is set while a worker is INITIALIZING with none running (not billed; a warning is sent after 30 min).
	InitializingSince *string `json:"initializing_since" doc:"RFC 3339 UTC; null unless a worker is initializing"`
}

// WorkerCountsOrNull may be null.
type WorkerCountsOrNull WorkerCounts

type workerCountsShape WorkerCounts

// Schema implements huma.SchemaProvider.
func (WorkerCountsOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[workerCountsShape](r)
}

// DriftView is one endpoint field that differs from the desired spec.
type DriftView struct {
	Field string `json:"field"`
	Want  string `json:"want"`
	Got   string `json:"got"`
}

// EndpointView is the endpoint configuration without its id.
type EndpointView struct {
	Name             string      `json:"name"`
	WorkersMin       int         `json:"workers_min"`
	WorkersMax       int         `json:"workers_max"`
	IdleTimeoutS     *int        `json:"idle_timeout_s"`
	GPUPools         []string    `json:"gpu_pools" nullable:"false"`
	ExcludedTypes    []string    `json:"excluded_types" nullable:"false"`
	Flashboot        string      `json:"flashboot"`
	ImageDigestShort string      `json:"image_digest_short"`
	Drift            []DriftView `json:"drift" nullable:"false"`
}

// EndpointOrNull may be null.
type EndpointOrNull EndpointView

type endpointShape EndpointView

// Schema implements huma.SchemaProvider.
func (EndpointOrNull) Schema(r huma.Registry) *huma.Schema { return nullableSchema[endpointShape](r) }

// ProviderCapabilities are what the configured keys allow.
type ProviderCapabilities struct {
	ScaleToZero bool   `json:"scale_to_zero"`
	RevertDrift bool   `json:"revert_drift"`
	Billing     string `json:"billing" enum:"hour,none"`
}

// ProviderPrice is the list price in use.
type ProviderPrice struct {
	PerHourMicroUSD int64  `json:"per_hour_micro_usd"`
	PerHourUSD      string `json:"per_hour_usd"`
	ObservedAt      string `json:"observed_at"`
}

// ProviderPriceOrNull may be null.
type ProviderPriceOrNull ProviderPrice

type providerPriceShape ProviderPrice

// Schema implements huma.SchemaProvider.
func (ProviderPriceOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[providerPriceShape](r)
}

// RTFView is the real-time factor of one GPU class.
type RTFView struct {
	GPUClass string `json:"gpu_class"`
	RTFMilli int    `json:"rtf_milli"`
	Samples  int    `json:"samples"`
}

// MeterView summarises the worker meter of the last 24 hours.
type MeterView struct {
	Lifetimes24h      int `json:"lifetimes_24h"`
	BilledSeconds24h  int `json:"billed_seconds_24h"`
	MaxWorkersSeen24h int `json:"max_workers_seen_24h"`
}

// MeterOrNull may be null.
type MeterOrNull MeterView

type meterShape MeterView

// Schema implements huma.SchemaProvider.
func (MeterOrNull) Schema(r huma.Registry) *huma.Schema { return nullableSchema[meterShape](r) }

// ProviderStatus never contains the endpoint id, keys, provider refs or worker ids.
type ProviderStatus struct {
	Backend              string               `json:"backend"`
	GenerationEnabled    bool                 `json:"generation_enabled"`
	DisabledReason       *string              `json:"disabled_reason"`
	Armed                bool                 `json:"armed"`
	ArmedChangedBy       PrincipalRef         `json:"armed_changed_by"`
	ArmedChangedAt       string               `json:"armed_changed_at"`
	Trip                 *TripOrNull          `json:"trip"`
	TripsRecent          []TripView           `json:"trips_recent" nullable:"false" doc:"At most 5"`
	LastReconcileAt      *string              `json:"last_reconcile_at"`
	Workers              *WorkerCountsOrNull  `json:"workers"`
	Endpoint             *EndpointOrNull      `json:"endpoint"`
	Capabilities         ProviderCapabilities `json:"capabilities"`
	Price                *ProviderPriceOrNull `json:"price"`
	RTF                  []RTFView            `json:"rtf" nullable:"false"`
	ColdStartS           int                  `json:"cold_start_s"`
	KillSlackS           int                  `json:"kill_slack_s"`
	MaxRuntimeS          int                  `json:"max_runtime_s"`
	TTLS                 int                  `json:"ttl_s"`
	PriceMarginPPM       int64                `json:"price_margin_ppm"`
	Meter                *MeterOrNull         `json:"meter"`
	QuoteObservedAt      *string              `json:"quote_observed_at"`
	SkewMS               *int64               `json:"skew_ms"`
	RestoredUnreconciled bool                 `json:"restored_unreconciled"`
	Balance              Balance              `json:"balance"`
}

// ---- lineage ----

// LineageNode is one track of a lineage tree.
type LineageNode struct {
	ID            string                  `json:"id"`
	ParentID      *string                 `json:"parent_id"`
	Generation    int                     `json:"generation"`
	Title         string                  `json:"title"`
	Status        string                  `json:"status"`
	Seed          *int64                  `json:"seed" doc:"null for a reference track (recipe unknown)"`
	RecipeKnown   bool                    `json:"recipe_known"`
	Lossy         bool                    `json:"lossy"`
	RequestID     string                  `json:"request_id"`
	Origin        string                  `json:"origin"`
	Operator      *string                 `json:"operator"`
	Inferred      bool                    `json:"inferred"`
	Rationale     *string                 `json:"rationale"`
	Stars         *int                    `json:"stars"`
	CriticStars   *int                    `json:"critic_stars"`
	Outcome       *string                 `json:"outcome"`
	Cost          *library.GenerationCost `json:"cost"`
	JobID         *string                 `json:"job_id"`
	CreatedAt     string                  `json:"created_at"`
	ChildrenCount int                     `json:"children_count"`
}

// LineageTree is the tree around a track, in pre-order.
type LineageTree struct {
	RootID    string        `json:"root_id"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated" doc:"Cut at limit or at the page byte cap"`
	Nodes     []LineageNode `json:"nodes" nullable:"false"`
}

// ---- inputs ----

type estimateGenerationIn struct {
	Body struct {
		Engine *string  `json:"engine,omitempty" enum:"yue2" doc:"Default yue2"`
		Items  []ItemIn `json:"items" minItems:"1" maxItems:"20"`
	}
}

type estimateVariantIn struct {
	ID   string `path:"id" doc:"The mother track"`
	Body struct {
		Mode   string  `json:"mode" enum:"new_seed,same_seed_edit,lyric_edit,style_edit"`
		NSeeds int     `json:"n_seeds,omitempty" minimum:"1" maximum:"10"`
		Seeds  []int64 `json:"seeds,omitempty" nullable:"false" minItems:"1" maxItems:"10"`
		Patch  *Patch  `json:"patch,omitempty"`
		Title  *string `json:"title,omitempty" minLength:"1" maxLength:"200"`
	}
}

// ConfirmBody confirms an estimate. create_generation and its human alias share it.
type ConfirmBody struct {
	ConfirmationToken string  `json:"confirmation_token" pattern:"^mmc_[a-z2-7]{52}$" doc:"From the estimate; single use"`
	Rationale         *string `json:"rationale,omitempty" minLength:"1" maxLength:"1000" doc:"Why; stored on the batch and the Operator Ledger"`
}

type createGenerationIn struct {
	IdemHeader
	Body ConfirmBody
}

type createVariantIn struct {
	IdemHeader
	ID   string `path:"id" doc:"The mother track of the estimate"`
	Body ConfirmBody
}

type batchIDIn struct {
	ID string `path:"id" doc:"Batch id"`
}

type reasonBody struct {
	Reason *string `json:"reason,omitempty" minLength:"1" maxLength:"500"`
}

type approveBatchIn struct {
	ID   string     `path:"id" doc:"Batch id"`
	Body reasonBody `required:"false"`
}

type rejectBatchIn struct {
	ID   string `path:"id" doc:"Batch id"`
	Body struct {
		Reason string `json:"reason" minLength:"1" maxLength:"500"`
	}
}

type listBatchesIn struct {
	State         []string `query:"state,explode" nullable:"false" enum:"awaiting_confirmation,active,finished,rejected,expired"`
	PrincipalKind []string `query:"principal_kind,explode" nullable:"false" enum:"human,agent"`
	Since         string   `query:"since" doc:"RFC 3339"`
	Until         string   `query:"until" doc:"RFC 3339"`
	Limit         int      `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor        string   `query:"cursor"`
}

type cancelIn struct {
	ID   string     `path:"id" doc:"Job or batch id"`
	Body reasonBody `required:"false"`
}

type listJobsIn struct {
	State   []string `query:"state,explode" nullable:"false" enum:"awaiting_confirmation,queued,submitting,starting,generating,transferring,ingesting,cancelling,failing,completed,failed,cancelled,expired"`
	BatchID string   `query:"batch_id"`
	TrackID string   `query:"track_id" doc:"Jobs with this track as parent or child"`
	Since   string   `query:"since" doc:"RFC 3339"`
	Limit   int      `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor  string   `query:"cursor"`
}

type jobIDIn struct {
	ID string `path:"id" doc:"Job id (= the take id)"`
}

type waitForJobIn struct {
	ID         string `path:"id" doc:"Job id"`
	SinceState string `query:"since_state" enum:"awaiting_confirmation,queued,submitting,starting,generating,transferring,ingesting,cancelling,failing,completed,failed,cancelled,expired" doc:"Return as soon as the state differs; default the state at call time"`
	TimeoutS   int    `query:"timeout_s" minimum:"0" maximum:"55" default:"25"`
}

type getSpendIn struct {
	Month string `query:"month" pattern:"^[0-9]{4}-(0[1-9]|1[0-2])$" doc:"YYYY-MM; default the current month"`
}

type listLedgerIn struct {
	BatchID string   `query:"batch_id"`
	Kind    []string `query:"kind,explode" nullable:"false" enum:"reserve,settle,residual,release,bill"`
	Since   string   `query:"since" doc:"RFC 3339"`
	Until   string   `query:"until" doc:"RFC 3339"`
	Limit   int      `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Cursor  string   `query:"cursor"`
}

// TokenCapsIn sets one agent token's limits; null clears the override.
type TokenCapsIn struct {
	TokenID             string `json:"token_id" minLength:"1"`
	DailyBudgetMicroUSD *int64 `json:"daily_budget_micro_usd" required:"true" nullable:"true" minimum:"0" maximum:"1000000000"`
	AutoApproveTakes    *int   `json:"auto_approve_takes" required:"true" nullable:"true" minimum:"0" maximum:"100"`
	AutoApproveMicroUSD *int64 `json:"auto_approve_micro_usd" required:"true" nullable:"true" minimum:"0" maximum:"1000000000"`
}

type setSpendCapsIn struct {
	Body struct {
		MonthlyMicroUSD     *int64        `json:"monthly_micro_usd,omitempty" minimum:"0" maximum:"1000000000"`
		BatchMicroUSD       *int64        `json:"batch_micro_usd,omitempty" minimum:"0" maximum:"1000000000"`
		HumanDailyMicroUSD  *int64        `json:"human_daily_micro_usd,omitempty" minimum:"0" maximum:"1000000000"`
		AgentDailyMicroUSD  *int64        `json:"agent_daily_micro_usd,omitempty" minimum:"0" maximum:"1000000000"`
		AutoApproveTakes    *int          `json:"auto_approve_takes,omitempty" minimum:"0" maximum:"100"`
		AutoApproveMicroUSD *int64        `json:"auto_approve_micro_usd,omitempty" minimum:"0" maximum:"1000000000"`
		Tokens              []TokenCapsIn `json:"tokens,omitempty" nullable:"false" maxItems:"50"`
	}
}

type updateProviderStateIn struct {
	Body struct {
		Armed     *bool  `json:"armed,omitempty"`
		ResetTrip *bool  `json:"reset_trip,omitempty" doc:"Clear the open trip (no-op when none is open)"`
		Reason    string `json:"reason" minLength:"1" maxLength:"500"`
	}
}

type getLineageIn struct {
	ID    string `path:"id" doc:"Any track of the tree"`
	Limit int    `query:"limit" minimum:"1" maximum:"500" default:"200"`
}

// ---- outputs ----

type estimateOut struct{ Body Estimate }
type batchOut struct{ Body Batch }
type approvalOut struct{ Body ApprovalStatus }
type batchPageOut struct{ Body BatchPage }
type jobOut struct{ Body Job }
type jobPageOut struct{ Body JobPage }
type waitJobOut struct{ Body WaitJobResult }
type budgetOut struct{ Body Budget }
type spendOut struct{ Body Spend }
type ledgerPageOut struct{ Body LedgerPage }
type capsOut struct{ Body CapsView }
type providerStatusOut struct{ Body ProviderStatus }
type lineageOut struct{ Body LineageTree }
