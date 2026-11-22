package pods

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Signer mints and verifies the poll link of a pod (C395.1): POST /worker/v1/pods/{pod_id}/poll?exp=<unix>&sig=<mac>. The key is
// Secrets.PodPollKey() (HMAC-SHA256 of the secret key, already derived); the link is the credential, bound to one pod and one expiry.
type Signer struct{ key []byte }

// MinKeyLen is the shortest key a Signer accepts: the derived pod key is an HMAC-SHA256 output (32 bytes).
const MinKeyLen = 32

// NewSigner wraps the derived pod-poll key. A key shorter than MinKeyLen (an absent key is empty) yields a signer that verifies nothing:
// the poll link is the only credential on its route, so a missing key must fail closed.
func NewSigner(pollKey []byte) *Signer { return &Signer{key: append([]byte(nil), pollKey...)} }

func (s *Signer) sig(podID string, exp int64) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("v1|pod|" + podID + "|" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Verify checks a signature in constant time.
func (s *Signer) Verify(podID string, exp int64, sig string) bool {
	if s == nil || len(s.key) < MinKeyLen {
		return false
	}
	return hmac.Equal([]byte(s.sig(podID, exp)), []byte(sig))
}

// URL is `<publicURL>/worker/v1/pods/<pod_id>/poll?exp=<unix>&sig=<b64url>`; it goes into the pod's environment at create and nowhere
// else (never stored, logged, evented or returned).
func (s *Signer) URL(publicURL, podID string, exp int64) string {
	q := url.Values{"exp": {strconv.FormatInt(exp, 10)}, "sig": {s.sig(podID, exp)}}
	return strings.TrimRight(publicURL, "/") + "/worker/v1/pods/" + podID + "/poll?" + q.Encode()
}

// Queue is the in-memory hand-out queue (C395.4, C396.1): the takes the pod backend accepted that no pod has fetched yet, per batch,
// oldest first. It holds TakeSpecs, whose signed upload URLs are never persisted, so a restart drops the queue (the assignments
// stay in the database and the runner resolves them). Safe for concurrent use.
type Queue struct {
	mu      sync.Mutex
	byBatch map[string][]queued
	cancels map[string]struct{} // handed attempts the runner cancelled
	changed chan struct{}       // closed and replaced on every change
}

type queued struct {
	batchID string
	spec    provider.TakeSpec
}

// NewQueue returns an empty queue.
func NewQueue() *Queue {
	return &Queue{byBatch: map[string][]queued{}, cancels: map[string]struct{}{}, changed: make(chan struct{})}
}

func (q *Queue) notifyLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

// Changed returns a channel that is closed on the next change (an Add, a Drop, a Requeue or a cancel request).
func (q *Queue) Changed() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.changed
}

// Add appends a take of a batch and wakes pollers.
func (q *Queue) Add(batchID string, t provider.TakeSpec) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.byBatch[batchID] = append(q.byBatch[batchID], queued{batchID, t})
	q.notifyLocked()
}

// Requeue puts a take back at the front of its batch (a hand-out that could not be recorded).
func (q *Queue) Requeue(batchID string, t provider.TakeSpec) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.byBatch[batchID] = append([]queued{{batchID, t}}, q.byBatch[batchID]...)
	q.notifyLocked()
}

// Len is the number of unhanded takes of a batch.
func (q *Queue) Len(batchID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.byBatch[batchID])
}

// TakeResult is the outcome of Take.
type TakeResult int

// The outcomes of Take.
const (
	TakeEmpty TakeResult = iota // nothing queued for the batch
	TakeUnfit                   // the oldest take does not fit (the caller's predicate refused it); it stays queued
	TakeOK
)

// Take removes and returns the oldest take of a batch when fit accepts it. Two pollers can never get the same take.
func (q *Queue) Take(batchID string, fit func(provider.TakeSpec) bool) (provider.TakeSpec, TakeResult) {
	q.mu.Lock()
	defer q.mu.Unlock()
	l := q.byBatch[batchID]
	if len(l) == 0 {
		return provider.TakeSpec{}, TakeEmpty
	}
	if fit != nil && !fit(l[0].spec) {
		return provider.TakeSpec{}, TakeUnfit
	}
	t := l[0].spec
	if len(l) == 1 {
		delete(q.byBatch, batchID)
	} else {
		q.byBatch[batchID] = l[1:]
	}
	return t, TakeOK
}

// Has reports whether an unhanded take of the attempt is queued.
func (q *Queue) Has(attemptID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, l := range q.byBatch {
		for _, e := range l {
			if e.spec.AttemptID == attemptID {
				return true
			}
		}
	}
	return false
}

// Drop removes an unhanded take by attempt id (the runner cancelled it before any pod fetched it) and reports whether it was queued.
func (q *Queue) Drop(attemptID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for b, l := range q.byBatch {
		for i, e := range l {
			if e.spec.AttemptID == attemptID {
				l = append(l[:i:i], l[i+1:]...)
				if len(l) == 0 {
					delete(q.byBatch, b)
				} else {
					q.byBatch[b] = l
				}
				q.notifyLocked()
				return true
			}
		}
	}
	return false
}

// Purge drops every unhanded take and returns their attempt ids (the backend's PurgeQueue).
func (q *Queue) Purge() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var ids []string
	for _, l := range q.byBatch {
		for _, e := range l {
			ids = append(ids, e.spec.AttemptID)
		}
	}
	q.byBatch = map[string][]queued{}
	q.notifyLocked()
	return ids
}

// RequestCancel records that the runner cancelled a handed take; the next poll of its pod lists it in `cancel`.
func (q *Queue) RequestCancel(attemptID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancels[attemptID] = struct{}{}
	q.notifyLocked()
}

// ClearCancel forgets a cancel request once its attempt is closed (done or lost), so the map does not grow for the process lifetime.
func (q *Queue) ClearCancel(attemptID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.cancels, attemptID)
}

// CancelRequested reports whether RequestCancel was called for the attempt.
func (q *Queue) CancelRequested(attemptID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.cancels[attemptID]
	return ok
}
