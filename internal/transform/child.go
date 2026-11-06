package transform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The render child contract (C210.4). The supervisor (supervisor.go) and the test fake (transformtest.FakeChild) both
// implement ChildRunner; the bridge never knows which one it drives.

// Child exit codes (C210.4).
const (
	ExitOK       = 0
	ExitInvalid  = 10 // invalid job
	ExitBudget   = 11 // work budget exceeded
	ExitNonFinit = 12 // non-finite or silent output
	ExitInternal = 13
)

// Fixed file names of a work dir; the child opens only these, through os.Root.
const (
	jobFile    = "job.json"
	outFile    = "out.pcm24"
	reportFile = "report.json"
	flacFile   = "out.flac"
)

func inFile(n int) string { return fmt.Sprintf("in-%d.f32", n) }

// ChildJob is one child run: the absolute work dir (mode 0700, holds job.json and the decoded inputs), the memory estimate the
// GOMEMLIMIT and the RSS guard derive from, and the wall deadline.
type ChildJob struct {
	Dir      string
	MemMB    int
	Deadline time.Duration
}

// ChildProgress receives throttled progress: permille 0..1000 and the phase name.
type ChildProgress func(permille int, phase string)

// ChildResult is what a child that ran to an exit reports. CPU is its user+system time.
type ChildResult struct {
	ExitCode   int
	CPU        time.Duration
	StderrTail string // already redacted by the runner; at most 4 KiB
}

// Sentinels of a supervised run that did not reach a normal exit.
var (
	ErrChildDeadline = errors.New("transform: child wall deadline")
	ErrChildMemory   = errors.New("transform: child memory guard")
	// ErrChildSignaled: the child died of a signal the supervisor did not send (the kernel's OOM killer, an operator's kill).
	// That is an interruption, not a verdict on the job: the row goes back to the queue (C216.5).
	ErrChildSignaled = errors.New("transform: child killed by a signal")
)

// ChildRunner runs the render child. It returns a nil error for any normal exit (the code is in the result), ErrChildDeadline
// or ErrChildMemory when the supervisor's own guard killed the run, ErrChildSignaled when a signal from outside did, and the context's error when ctx ended (the child is dead by then).
// CPU is filled whenever the process was reaped, also after a kill.
type ChildRunner interface {
	Run(ctx context.Context, j ChildJob, progress ChildProgress) (ChildResult, error)
}

// jobDoc is job.json (C210.4). Alignment carries the frozen resolution of the spec for the kinds that use it; the per-input
// tempo fields repeat it for the operators that only need a number.
type jobDoc struct {
	V          int             `json:"v"`
	Kind       Kind            `json:"kind"`
	Rate       int             `json:"rate"`
	Seed       int64           `json:"seed"`
	Params     json.RawMessage `json:"params"`
	Inputs     []jobInput      `json:"inputs"`
	Alignment  json.RawMessage `json:"alignment,omitempty"`
	UnitsMax   int64           `json:"units_max"`
	TargetLUFS float64         `json:"target_lufs"`
	RefLUFS    *float64        `json:"ref_lufs"`
	Out        string          `json:"out"`
	Report     string          `json:"report"`
}

type jobInput struct {
	Role        string   `json:"role"`
	File        string   `json:"file"`
	Frames      int64    `json:"frames"`
	BPM         *float64 `json:"bpm"`
	BeatsPerBar *int     `json:"beats_per_bar"`
	KeyShiftS   int      `json:"key_shift_s"`
	DownbeatS   *float64 `json:"downbeat_s"`
}

// childReport is report.json as the bridge reads it. The child owns the file; unknown keys are ignored and absent optional
// ones stay nil, so the report can grow without a bridge change.
type childReport struct {
	PCMSHA256  string            `json:"pcm_sha256"`
	Frames     int64             `json:"frames"`
	Units      int64             `json:"units"`
	Safety     safetyDoc         `json:"safety"`
	Warnings   []reportWarning   `json:"warnings"`
	BeatPhase  []json.RawMessage `json:"beat_phase"` // one entry per input, null when the kind measures none
	Voices     json.RawMessage   `json:"voices"`
	BlindSpots []string          `json:"blind_spots"`
}

type reportWarning struct {
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
}

type limiterDoc struct {
	MaxReductionDB  float64 `json:"max_reduction_db"`
	EngagedFraction float64 `json:"engaged_fraction"`
}

type safetyDoc struct {
	GainDB              float64    `json:"gain_db"`
	Limiter             limiterDoc `json:"limiter"`
	TargetLUFSRequested float64    `json:"target_lufs_requested"`
	TargetLUFSEffective float64    `json:"target_lufs_effective"`
	LUFSIn              float64    `json:"lufs_in"`
	LUFSOut             float64    `json:"lufs_out"`
	TruePeakDBTP        float64    `json:"true_peak_dbtp"`
}

// exitCode maps a child exit code to the row code (C210.6 error mapping).
func exitCode(code int) string {
	switch code {
	case ExitInvalid:
		return ErrInvalidTransform
	case ExitBudget:
		return ErrBudgetExceeded
	case ExitNonFinit:
		return ErrDSPNonFinite
	default:
		return ErrTransformFailed
	}
}

// nsPerUnit is the cost model's NSPerUnit (C209.2) in nanoseconds, from cost.go's DefaultCosts (the one place it lives): the
// work meter's budget is the CPU budget less the tool shares, converted at this rate.
var nsPerUnit = float64(DefaultCosts.NSPerUnitMilli) / 1000

// unitsBudget is units_budget = floor((max_cpu_ms - tool/decode/encode/ingest shares) * 1e6 / NSPerUnit) of C209.3, never
// below one so a tight budget fails inside the child as ExitBudget rather than being unlimited.
func unitsBudget(maxCPUMS int64, inSeconds, outSeconds float64) int64 {
	share := 3*inSeconds + 10*outSeconds + 30*outSeconds // decode_ms + encode_ms + ingest_ms
	ms := float64(maxCPUMS) - share
	if ms < 1 {
		return 1
	}
	return int64(ms * 1e6 / nsPerUnit)
}

// wallDeadline is max(60 s, WallFactor x budget) (C200.4).
func wallDeadline(maxCPUMS int64) time.Duration {
	d := time.Duration(WallFactor*maxCPUMS) * time.Millisecond
	return max(d, 60*time.Second)
}
