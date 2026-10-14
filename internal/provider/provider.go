// Package provider defines the Backend interface behind generation jobs and the mm.worker/1 types (M3 C73).
// Implementations live in provider/fake (in-process backend and simulated worker) and provider/runpod
// (REST v2 client and serverless backend). The package imports no job code: jobs call backends, never the
// other way round (TestImportGraph_M3).
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// Backend is one place a take can be rendered.
type Backend interface {
	Name() string                                                          // "fake" | "runpod-serverless"
	Quote(ctx context.Context) (Quote, error)                              // list price, pools, endpoint config (reconciler only; never in a request path)
	Submit(ctx context.Context, t TakeSpec) (Ref, error)                   // one provider job; NOT idempotent, never replayed (D-M3-4)
	Poll(ctx context.Context, r Ref) (RemoteStatus, error)                 //
	Cancel(ctx context.Context, r Ref) error                               // returns after the POST was acknowledged; confirmation comes from Poll
	Sweep(ctx context.Context) (SweepReport, error)                        // workers, endpoint config, drift, Date-header skew
	PurgeQueue(ctx context.Context) error                                  //
	ScaleToZero(ctx context.Context) error                                 // ErrNotPermitted without a scoped write key
	RevertDrift(ctx context.Context, d []Drift) error                      // ErrNotPermitted likewise
	Billing(ctx context.Context, from, to time.Time) ([]BillBucket, error) // hourly; ErrNotPermitted when unreadable
	Capabilities() Capabilities                                            // from key presence and key-capabilities.json
}

var (
	// ErrNotPermitted: the configured keys cannot do this.
	ErrNotPermitted = errors.New("provider: not permitted with the configured keys")
	// ErrAccess: access refused (401/403/1010); trips provider_access (C76.3).
	ErrAccess = errors.New("provider: access refused (401/403/1010)")
	// ErrEndpointNotFound is a provider fault, never job loss.
	ErrEndpointNotFound = errors.New("provider: endpoint not found")
)

// Backend names.
const (
	BackendFake   = "fake"
	BackendRunPod = "runpod-serverless"
)

// Quote is the provider's current price and endpoint configuration.
type Quote struct {
	Backend           string
	ListPricePPH      money.MicroUSD
	Pools, GPUClasses []string
	Endpoint          EndpointConfig
	ObservedAt        time.Time
	// SweepUnreadable: the read key could not GET the endpoint or list workers (C68 reason sweep_unreadable).
	SweepUnreadable bool
}

// EndpointConfig is the endpoint as read from the provider (C77.5); the desired values live in
// runpod.DesiredSpec.
type EndpointConfig struct {
	Name, Type              string // Type "QUEUE"
	ScalerType, ScalerValue string // "QUEUE_DELAY", decimal literal of queueDelay
	WorkersMin, WorkersMax  int
	IdleTimeoutS            *int // nil means absent (fails closed)
	TimeoutMS               int64
	GPUCount                int
	MinCUDA                 string
	Pools, ExcludedTypes    []string
	Flashboot               string
	ImageDigest             string
	RequestURLs             map[string]string // run, status, cancel, ... (host checked, C77.5)
}

// UploadTarget is one capability URL the worker PUTs an artifact to. URL is never persisted, logged or
// returned.
type UploadTarget struct {
	URL      string `json:"url"`
	MaxBytes int64  `json:"max_bytes"`
}

// WorkerExpect carries the pinned weight revisions (repo to revision) from yue2.Weights.
type WorkerExpect struct {
	Weights map[string]string `json:"weights"`
}

// Output is the worker's result (C73.5), decoded strictly (DisallowUnknownFields).
type Output struct {
	Contract     string                    `json:"contract"`
	TakeID       string                    `json:"take_id"`
	AttemptID    string                    `json:"attempt_id"`
	Status       string                    `json:"status"` // "ok" | "error"
	Error        *OutputError              `json:"error"`
	Artifacts    map[string]ArtifactDigest `json:"artifacts"`
	AudioSeconds *json.Number              `json:"audio_seconds"`
	Truncated    map[string]bool           `json:"truncated"`
	Timings      map[string]*json.Number   `json:"timings"`
	Env          OutputEnv                 `json:"env"`
}

// OutputError is the error object of a failed worker run.
type OutputError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ArtifactDigest is what the worker reports about one uploaded file.
type ArtifactDigest struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// OutputEnv is the environment fingerprint the worker reports.
type OutputEnv struct {
	Engine      string            `json:"engine"`
	PkgVersion  string            `json:"pkg_version"`
	CodeCommit  string            `json:"code_commit"`
	Weights     map[string]string `json:"weights"`
	Decoder     string            `json:"decoder"`
	GPUName     string            `json:"gpu_name"`
	CUDA        string            `json:"cuda"`
	Torch       string            `json:"torch"`
	WorkerBuild string            `json:"worker_build"`
	Python      string            `json:"python"`
}

// TakeSpec is everything a backend needs to submit one attempt.
type TakeSpec struct {
	JobID, AttemptID, Engine string
	Request                  json.RawMessage // canonical engine-native request
	TimeoutS                 int
	TTL                      time.Duration
	UploadBudgetS            int
	Uploads                  map[string]UploadTarget // artifact to target; minted at submit, never persisted or logged
	StartURL                 string                  // D-M3-28; minted at submit, never persisted or logged
	Expect                   WorkerExpect            // pinned weight revisions
	Deadline                 time.Time               // = window_ends_at
}

// Ref identifies a provider job. ID never leaves the server (API, events, logs).
type Ref struct{ Backend, ID string }

// RemoteState is the provider-neutral job status (C73.2).
type RemoteState string

// Remote states.
const (
	Queued    RemoteState = "Queued"
	Running   RemoteState = "Running"
	Completed RemoteState = "Completed"
	Failed    RemoteState = "Failed"
	Cancelled RemoteState = "Cancelled"
	TimedOut  RemoteState = "TimedOut"
	NotFound  RemoteState = "NotFound"
	Unknown   RemoteState = "Unknown"
)

// RemoteStatus is one observation of a provider job.
type RemoteStatus struct {
	State                RemoteState
	Raw                  string          // the provider's status string (Unknown keeps it for the event)
	ExecutionMS, DelayMS *int64          // nil means absent in the record
	WorkerID             string          // opaque; hashed to worker_key before storage (C76.6)
	Output               json.RawMessage // at most 64 KiB; the worker output (C73.5) when present
	ErrorText            string          // redacted, at most 500 bytes; stored only in job_events.detail_json
}

// Capabilities are what the configured keys allow.
type Capabilities struct {
	ScaleToZero, RevertDrift bool
	Billing                  string // "hour" | "none"
}

// Worker is one worker record of a sweep.
type Worker struct {
	ID        string // opaque; hashed
	Status    string
	UptimeS   *int // nil until placed
	StartedAt *time.Time
	Image     string
	IsStale   bool
}

// Drift is one endpoint field that differs from the desired spec.
type Drift struct{ Field, Want, Got string }

// SweepReport is the result of Sweep.
type SweepReport struct {
	Workers    []Worker
	Endpoint   *EndpointConfig
	Drift      []Drift
	ObservedAt time.Time
	Skew       *time.Duration
}

// BillBucket is one hourly billing bucket; Compute = gpuAmount + cpuAmount.
type BillBucket struct {
	Start, End           time.Time
	Total, Compute, Disk money.MicroUSD
}
