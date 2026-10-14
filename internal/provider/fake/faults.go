package fake

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Code is the class of a refused submit. The values equal runpod.Code so the runner treats both backends
// alike (C77.4).
type Code string

// Error codes.
const (
	EdgeBlocked    Code = "EDGE_BLOCKED"
	Unauthorized   Code = "UNAUTHORIZED"
	Forbidden      Code = "FORBIDDEN"
	NotFound       Code = "NOT_FOUND"
	EndpointPaused Code = "ENDPOINT_PAUSED"
	RateLimited    Code = "RATE_LIMITED"
	BadRequest     Code = "BAD_REQUEST"
	Upstream       Code = "UPSTREAM"
	Network        Code = "NETWORK"
)

// Error is a classified provider error with the method set of runpod.Error (Retryable, Ambiguous,
// Access), so the runner needs no backend-specific code. Like runpod.Error it carries no URL and no key.
type Error struct {
	Op         string
	Status     int
	Code       Code
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	return "fake: " + e.Op + ": " + string(e.Code) + " (" + strconv.Itoa(e.Status) + ")"
}

// Retryable reports whether the same call may succeed later: EndpointPaused, RateLimited, Upstream, Network.
func (e *Error) Retryable() bool {
	switch e.Code {
	case EndpointPaused, RateLimited, Upstream, Network:
		return true
	}
	return false
}

// Ambiguous reports that the job may exist anyway: Upstream and Network on a submit may have created the job (D-M3-4).
func (e *Error) Ambiguous() bool { return e.Code == Upstream || e.Code == Network }

// Access reports a refused credential or edge block: EdgeBlocked, Unauthorized, Forbidden.
func (e *Error) Access() bool {
	return e.Code == EdgeBlocked || e.Code == Unauthorized || e.Code == Forbidden
}

// Is makes access errors match provider.ErrAccess.
func (e *Error) Is(target error) bool { return target == provider.ErrAccess && e.Access() }

var errPatchFailed = errors.New("fake: PATCH failed (500)")

// markers are the C78.2 fault markers found in a recipe's style.
type markers struct {
	slow               time.Duration // 0 = not set
	cold               time.Duration
	hasCold            bool
	failWorkerError    bool
	failWorkerLost     bool
	truncAbc, truncSem bool
	shaMismatch        bool
	queueExpire        int
	submit             string
	uploadClosed       bool
	stuckWorker        bool
	cancelIgnored      int
	cancelAckKeeps     bool
	requeue            bool
	runBetweenPolls    bool
	noStart            bool
	sameAudio          bool
}

const maxMarkerSeconds = 1e6

// parseMarkers reads the tokens (case-sensitive, whitespace- or comma-separated) of a style string.
// Unknown fake: tokens are ignored.
func parseMarkers(style string) markers {
	var m markers
	for _, tok := range strings.FieldsFunc(style, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		rest, ok := strings.CutPrefix(tok, "fake:")
		if !ok {
			continue
		}
		key, val, _ := strings.Cut(rest, "=")
		secs := func() (time.Duration, bool) {
			f, err := strconv.ParseFloat(val, 64)
			if err != nil || f < 0 || f > maxMarkerSeconds {
				return 0, false
			}
			return time.Duration(f * float64(time.Second)), true
		}
		num := func() int {
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 || n > 1000 {
				return 0
			}
			return n
		}
		switch key {
		case "slow":
			if d, ok := secs(); ok {
				m.slow = d
			}
		case "cold":
			if d, ok := secs(); ok {
				m.cold, m.hasCold = d, true
			}
		case "fail":
			switch val {
			case "worker_error":
				m.failWorkerError = true
			case "worker_lost":
				m.failWorkerLost = true
			}
		case "truncate":
			switch val {
			case "abc":
				m.truncAbc = true
			case "semantic":
				m.truncSem = true
			}
		case "sha_mismatch":
			m.shaMismatch = true
		case "queue_expire":
			m.queueExpire = num()
		case "submit":
			switch val {
			case "429", "5xx", "paused", "1010", "network", "accepted_5xx":
				m.submit = val
			}
		case "upload_closed":
			m.uploadClosed = true
		case "stuck_worker":
			m.stuckWorker = true
		case "cancel_ignored":
			m.cancelIgnored = num()
		case "cancel_ack_keeps_running":
			m.cancelAckKeeps = true
		case "requeue":
			m.requeue = true
		case "run_between_polls":
			m.runBetweenPolls = true
		case "no_start":
			m.noStart = true
		case "same_audio":
			m.sameAudio = true
		}
	}
	return m
}

// styleOf extracts the recipe style from a canonical request; "" when it has none.
func styleOf(request json.RawMessage) string {
	var r struct {
		Style string `json:"style"`
	}
	if json.Unmarshal(request, &r) != nil {
		return ""
	}
	return r.Style
}

// submitFault is the error a first-attempt submit marker produces; nil for no fault (accepted_5xx is
// handled by the caller because the job exists).
func submitFault(marker string) *Error {
	switch marker {
	case "429":
		return &Error{Op: "submit", Status: 429, Code: RateLimited, RetryAfter: 5 * time.Second}
	case "5xx":
		return &Error{Op: "submit", Status: 503, Code: Upstream}
	case "paused":
		return &Error{Op: "submit", Status: 409, Code: EndpointPaused}
	case "1010":
		return &Error{Op: "submit", Status: 403, Code: EdgeBlocked}
	case "network":
		return &Error{Op: "submit", Code: Network}
	}
	return nil
}

func acceptedThenFailed() *Error { return &Error{Op: "submit", Status: 503, Code: Upstream} }
