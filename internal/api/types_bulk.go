package api

// Pod bulk views (BULK-P6, C398.3, C398.4). Owner-only: none of these ever carries a RunPod pod id, a poll URL, a signature or a key.

// PodCost is the money of one pod. Reserved is the overhead reserved when the pod was created; Settled is the lifetime at the frozen
// price once the pod's batch settled it, null before.
type PodCost struct {
	ReservedMicroUSD int64   `json:"reserved_micro_usd"`
	ReservedUSD      string  `json:"reserved_usd"`
	SettledMicroUSD  *int64  `json:"settled_micro_usd"`
	SettledUSD       *string `json:"settled_usd"`
}

// PodSummary is one pod of a bulk batch. Never the provider reference, the poll URL or a key.
type PodSummary struct {
	ID           string  `json:"id"`
	Name         string  `json:"name" doc:"mm-<12 hex of the id>; the name the pod carries at the provider"`
	Kind         string  `json:"kind" enum:"batch,canary"`
	BatchID      string  `json:"batch_id"`
	State        string  `json:"state" enum:"intent,creating,booting,ready,busy,orphaned,terminating,terminated"`
	GPUTry       int     `json:"gpu_try" doc:"Which create attempt of this pod this is (1..4)"`
	CreatedAt    string  `json:"created_at"`
	BootedAt     *string `json:"booted_at"`
	FirstPollAt  *string `json:"first_poll_at"`
	LastPollAt   *string `json:"last_poll_at"`
	DeadlineAt   string  `json:"deadline_at"`
	TerminatedAt *string `json:"terminated_at"`
	LifetimeS    *int    `json:"lifetime_s"`
	ErrorCode    *string `json:"error_code"`
	Cost         PodCost `json:"cost"`
}

// PodList is the answer of list_pods.
type PodList struct {
	Items []PodSummary `json:"items" nullable:"false"`
}

type listPodsIn struct {
	BatchID string `query:"batch_id" maxLength:"64" pattern:"^[A-Za-z0-9-]*$" doc:"Only the pods of this batch"`
	State   string `query:"state" enum:"live,all" default:"live" doc:"live: not yet terminated"`
	Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type listPodsOut struct{ Body PodList }

// PodWindow is the armed bulk window of the pod backend.
type PodWindow struct {
	ArmedAt      string `json:"armed_at"`
	AutoDisarmAt string `json:"auto_disarm_at" doc:"armed_at plus MM_POD_WINDOW_MAX; the loop disarms and stops pods then"`
}

// PodsStatus is the pods block of a pod backend's entry in get_provider_status.
type PodsStatus struct {
	KeyPresent          bool         `json:"key_present"`
	Probe               string       `json:"probe" enum:"ok,too_broad,failed,missing" doc:"The latest probe of the pod key"`
	Safety              string       `json:"safety" enum:"verified,failed,inconclusive,unverified" doc:"The latest self-termination canary for the configured image digest"`
	SafetyDigestMatches bool         `json:"safety_digest_matches" doc:"The latest canary row names the configured image digest"`
	Live                []PodSummary `json:"live" nullable:"false"`
	Window              *PodWindow   `json:"window"`
	Alerts              []string     `json:"alerts" nullable:"false" doc:"Open pod alert codes: events of live pods and an open pod trip"`
}
