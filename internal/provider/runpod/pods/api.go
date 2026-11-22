package pods

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// API is the RunPod pod API the lifecycle drives (C392.1). Every method passes its errors through the redactor: none carries a
// URL, a pod id, the poll URL or a key.
type API interface {
	CreatePod(ctx context.Context, b CreateBody) (Pod, error)
	// GetPod returns ErrPodGone on a 404 or a TERMINATED status (the Pod is still returned in the second case).
	GetPod(ctx context.Context, id string) (Pod, error)
	// ListPods returns every page; the caller filters the mm- prefix.
	ListPods(ctx context.Context) ([]Pod, error)
	// DeletePod answers 204; a 404 is success.
	DeletePod(ctx context.Context, id string) error
	// CancelQueued is DELETE /v2/queued-pods/{id}, only after an unexpected 202; a 404 is success.
	CancelQueued(ctx context.Context, id string) error
	// PodLogsTail reads the container log (server-sent events) for at most LogsReadTimeout and returns the last n lines.
	PodLogsTail(ctx context.Context, id string, n int) ([]string, error)
	// CatalogSecurePrices returns price.secure (USD per hour) of every requested type, or *PriceError when one is missing or zero.
	CatalogSecurePrices(ctx context.Context, typeIDs []string) (map[string]money.MicroUSD, error)
	// PodBilling returns hourly buckets summed over the given pods only (billing is account-wide; the rest is dropped).
	PodBilling(ctx context.Context, from, to time.Time, podIDs []string) ([]provider.BillBucket, error)
}

// Errors of C392.3. Sentinels are compared with errors.Is, the others with errors.As.
var (
	// ErrPodGone: the pod does not exist (404) or is TERMINATED.
	ErrPodGone = errors.New("pods: pod is gone")
	// ErrQueued: RunPod answered 202 (queued). No `queue` is ever sent, so this must not happen; the queue entry was cancelled.
	ErrQueued = errors.New("pods: create was queued")
	// ErrBalance: 402, insufficient balance. Stop the batch's pods; no retry.
	ErrBalance = errors.New("pods: insufficient balance")
	// ErrInvalid: 422, the body breaks the contract. Trips pod_create_invalid.
	ErrInvalid = errors.New("pods: create body rejected by the contract")
	// ErrNoKey: the pod key file is absent, so no call is made.
	ErrNoKey = fmt.Errorf("pods: the pod key file is absent: %w", provider.ErrNotPermitted)
)

// ErrRefused is a 400, 403 or 409 on create: try the next GPU type. A 400 may be capacity or a rule (RunPod gives no machine code).
type ErrRefused struct {
	Status   int
	Capacity bool // maybe: a 400 is either capacity or a cross-field rule
	Detail   string
}

func (e *ErrRefused) Error() string {
	return "pods: create refused (" + strconv.Itoa(e.Status) + ")" + detailSuffix(e.Detail)
}

// ErrRateLimited is a 429: the request was not processed; retry the same type after RetryAfter.
type ErrRateLimited struct{ RetryAfter time.Duration }

func (e *ErrRateLimited) Error() string {
	return "pods: rate limited (retry after " + e.RetryAfter.String() + ")"
}

// ErrAmbiguous is a 5xx, a timeout or a reset on a call that is never replayed (create, delete): the effect may have happened. The
// name sweep decides. Cause is the transport or context error, when there was one.
type ErrAmbiguous struct {
	Op     string
	Status int
	Detail string
	Cause  error
}

func (e *ErrAmbiguous) Error() string {
	s := "pods: " + e.Op + ": outcome unknown"
	if e.Status != 0 {
		s += " (" + strconv.Itoa(e.Status) + ")"
	}
	return s + detailSuffix(e.Detail)
}

// Unwrap exposes the cause (a context error, never a URL-carrying one).
func (e *ErrAmbiguous) Unwrap() error { return e.Cause }

// APIError is any other refused call: Status is the HTTP status, 0 for a transport failure of a retried GET.
type APIError struct {
	Op         string
	Status     int
	Detail     string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return "pods: " + e.Op + ": " + statusText(e.Status) + detailSuffix(e.Detail)
}

// PriceError reports a catalog price that is missing or not usable, so the quote is unusable (C392.4).
type PriceError struct{ TypeID string }

func (e *PriceError) Error() string {
	return "pods: no usable secure price for GPU type " + strconv.Quote(e.TypeID)
}

// accessError is a 401 or 403 on a call that is not a create: it matches provider.ErrAccess (trip pod_access).
type accessError struct {
	op     string
	status int
}

func (e *accessError) Error() string {
	return "pods: " + e.op + ": access refused (" + strconv.Itoa(e.status) + ")"
}
func (e *accessError) Is(target error) bool { return target == provider.ErrAccess }

func detailSuffix(d string) string {
	if d == "" {
		return ""
	}
	return ": " + d
}

func statusText(s int) string {
	if s == 0 {
		return "transport failure"
	}
	return "status " + strconv.Itoa(s)
}
