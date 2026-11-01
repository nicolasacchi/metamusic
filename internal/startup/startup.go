// Package startup tracks the derivation steps that serve runs before it opens the listener (taxonomy seed, first Settle).
// A step that fails persistently no longer ends the process: serve answers HTTP degraded, keeps retrying the step in the
// background and clears the issue when it succeeds. /healthz and get_system_info report the issues.
package startup

import (
	"sort"
	"sync"
	"time"
)

// StepIssue is one failing startup step. Code is a stable UPPER_SNAKE code; it never carries an error text, a path or a secret
// (the redacted error goes to the log only).
type StepIssue struct {
	Step     string `json:"step"`
	Code     string `json:"code"`
	Attempts int    `json:"attempts"`
	Since    string `json:"since"`
}

// Health is a concurrency-safe set of failing steps. A nil *Health reports healthy, so tests and the CLI need no instance.
type Health struct {
	mu     sync.Mutex
	now    func() time.Time
	issues map[string]*StepIssue
}

// New returns an empty Health; now nil means time.Now.
func New(now func() time.Time) *Health {
	if now == nil {
		now = time.Now
	}
	return &Health{now: now, issues: map[string]*StepIssue{}}
}

// Fail records one failed attempt of step with the given code (the first failure fixes Since).
func (h *Health) Fail(step, code string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if i, ok := h.issues[step]; ok {
		i.Attempts++
		i.Code = code
		return
	}
	h.issues[step] = &StepIssue{Step: step, Code: code, Attempts: 1, Since: h.now().UTC().Format(time.RFC3339)}
}

// Clear removes step's issue (the step succeeded).
func (h *Health) Clear(step string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.issues, step)
}

// Issues returns a copy of the failing steps ordered by name; never nil.
func (h *Health) Issues() []StepIssue {
	out := []StepIssue{}
	if h == nil {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, i := range h.issues {
		out = append(out, *i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Step < out[b].Step })
	return out
}

// Ready reports whether every startup step has succeeded.
func (h *Health) Ready() bool { return len(h.Issues()) == 0 }
