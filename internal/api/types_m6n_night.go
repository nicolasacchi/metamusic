package api

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// M6N-N3 (C377.2): the night shift's views and bodies. The settings carry no money (D-M6N-1): the mandate alone bounds spend.

// NightSettings is the night shift's behaviour.
type NightSettings struct {
	Enabled         bool               `json:"enabled" doc:"Off until the Owner enables it in the app"`
	At              string             `json:"at" doc:"Local time HH:MM (the server's configured zone)"`
	MaxTakes        int                `json:"max_takes" doc:"Takes per night before the mandate trims it, 1..12"`
	ExplorePPM      int                `json:"explore_ppm" doc:"Share of takes that explore, parts per million, 0..500000"`
	MinStars        int                `json:"min_stars" doc:"The best take's Owner stars a lineage needs, 1..5"`
	ListeningChecks string             `json:"listening_checks" enum:"on,off"`
	Halted          *NightHaltedOrNull `json:"halted" doc:"Set while a halt stands; enabling the shift again in the app clears it"`
	UpdatedBy       PrincipalRef       `json:"updated_by"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// NightHalted says who stopped the night shift and why.
type NightHalted struct {
	By     PrincipalRef `json:"by"`
	At     time.Time    `json:"at"`
	Reason string       `json:"reason"`
}

// NightHaltedOrNull is a NightHalted that may be null.
type NightHaltedOrNull NightHalted

type nightHaltedShape NightHalted

// Schema implements huma.SchemaProvider.
func (NightHaltedOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[nightHaltedShape](r)
}

// MandateOrNull is a Mandate that may be null.
type MandateOrNull Mandate

type mandateShape Mandate

// Schema implements huma.SchemaProvider.
func (MandateOrNull) Schema(r huma.Registry) *huma.Schema { return nullableSchema[mandateShape](r) }

// NightAuthority is what the night would spend under.
type NightAuthority struct {
	Mandate *MandateOrNull `json:"mandate" doc:"The live mandate, else a scheduled one; null when there is none"`
	Reason  *string        `json:"reason" enum:"no_mandate,mandate_scheduled" doc:"Why the night would spend nothing; null when a mandate is live" nullable:"true"`
}

// NightRun is one night as stored, with what its batch did.
type NightRun struct {
	Night           string  `json:"night" doc:"The local date of the night"`
	State           string  `json:"state" enum:"planned,running,done,skipped,halted,failed" doc:"running and done split the stored created by whether its batch has finished"`
	Reason          *string `json:"reason" nullable:"true" doc:"Why a night spent nothing or ended early (skip reasons: disabled, halted, previous_running, no_mandate, mandate_scheduled, caps, over_ceiling, ...)"`
	BatchID         *string `json:"batch_id" nullable:"true"`
	Takes           int     `json:"takes"`
	CeilingMicroUSD int64   `json:"ceiling_micro_usd" doc:"The batch's estimate ceiling"`
	CeilingUSD      string  `json:"ceiling_usd"`
	SettledMicroUSD int64   `json:"settled_micro_usd"`
	SettledUSD      string  `json:"settled_usd"`
	MorningCrateID  *string `json:"morning_crate_id" nullable:"true" doc:"The first Morning Crate built after the night started"`
}

// NightShift is get_night_shift.
type NightShift struct {
	Settings  NightSettings  `json:"settings"`
	Authority NightAuthority `json:"authority"`
	NextRunAt *time.Time     `json:"next_run_at" nullable:"true" doc:"Null while the shift is disabled or halted"`
	Runs      []NightRun     `json:"runs" nullable:"false" doc:"The last 14 nights, newest first"`
}

// NightPlanItem is one chosen take.
type NightPlanItem struct {
	Kind          string  `json:"kind" enum:"exploit,explore"`
	RootTrackID   string  `json:"root_track_id"`
	ParentTrackID *string `json:"parent_track_id" nullable:"true"`
	Mode          string  `json:"mode"`
	Patch         *string `json:"patch" nullable:"true" doc:"The style a style edit would apply"`
	Reason        string  `json:"reason"`
}

// NightDropped is a lineage left out.
type NightDropped struct {
	RootTrackID string `json:"root_track_id"`
	Reason      string `json:"reason"`
}

// NightQuote prices the plan.
type NightQuote struct {
	Takes            int    `json:"takes"`
	ExpectedMicroUSD int64  `json:"expected_micro_usd"`
	ExpectedUSD      string `json:"expected_usd"`
	MaxMicroUSD      int64  `json:"max_micro_usd"`
	MaxUSD           string `json:"max_usd"`
	CeilingMicroUSD  int64  `json:"ceiling_micro_usd"`
	CeilingUSD       string `json:"ceiling_usd"`
	TierB            bool   `json:"tier_b" doc:"Whether some planned take has a listening check (Tier B); false when listening checks are off, the mandate does not allow them, or the worker capability is closed"`
}

// NightQuoteOrNull is a NightQuote that may be null.
type NightQuoteOrNull NightQuote

type nightQuoteShape NightQuote

// Schema implements huma.SchemaProvider.
func (NightQuoteOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[nightQuoteShape](r)
}

// NightPlanMandate is the live mandate's verdict on the plan.
type NightPlanMandate struct {
	ID      string   `json:"id"`
	Covered bool     `json:"covered"`
	Reasons []string `json:"reasons" nullable:"false"`
}

// NightPlanMandateOrNull is a NightPlanMandate that may be null.
type NightPlanMandateOrNull NightPlanMandate

type nightPlanMandateShape NightPlanMandate

// Schema implements huma.SchemaProvider.
func (NightPlanMandateOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[nightPlanMandateShape](r)
}

// NightPlan is preview_night_shift: tonight's plan and price, computed and discarded.
type NightPlan struct {
	Night     string                  `json:"night" doc:"The local date of the next night"`
	Seed      string                  `json:"seed" doc:"Decimal; the same seed and library give the same plan"`
	Items     []NightPlanItem         `json:"items" nullable:"false"`
	Dropped   []NightDropped          `json:"dropped" nullable:"false"`
	Trimmed   []NightDropped          `json:"trimmed" nullable:"false" doc:"Takes cut so the mandate covers the night"`
	Quote     *NightQuoteOrNull       `json:"quote" doc:"Null when nothing was priced"`
	Mandate   *NightPlanMandateOrNull `json:"mandate" doc:"Null without a live mandate"`
	WouldSkip *string                 `json:"would_skip" nullable:"true" doc:"The reason a night run now would spend nothing; null when it would create a batch"`
}

type nightShiftOut struct{ Body NightShift }

type nightPlanOut struct{ Body NightPlan }

type nightSettingsOut struct{ Body NightSettings }

type updateNightShiftIn struct {
	Body struct {
		Enabled         *bool   `json:"enabled,omitempty" doc:"true needs a live mandate and clears a halt"`
		At              *string `json:"at,omitempty" doc:"HH:MM local"`
		MaxTakes        *int    `json:"max_takes,omitempty" doc:"1..12"`
		ExplorePPM      *int    `json:"explore_ppm,omitempty" doc:"0..500000"`
		MinStars        *int    `json:"min_stars,omitempty" doc:"1..5"`
		ListeningChecks *string `json:"listening_checks,omitempty" enum:"on,off"`
	}
}

type haltNightShiftIn struct {
	Body struct {
		Reason string `json:"reason" doc:"Why, 1..500 characters"`
	}
}
