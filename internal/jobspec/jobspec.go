// Package jobspec is the leaf vocabulary of a job's spec_json and needs_json (G1 C323.3): what a non-plain job carries beyond its
// recipe (pinned input audio, the Ears stage, the plan seeds, small attachments) and the capabilities a worker must have to run
// it. It imports the standard library and internal/canonjson only, so every layer (generation at create, jobs at claim and
// submit, a track's ingester) can use it without a cycle.
package jobspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/canonjson"
)

// The job kinds (jobs.kind CHECK, C323.1).
const (
	KindGenerate = "generate"
	KindAnalyze  = "analyze"
	KindPlan     = "plan"
)

// The capability vocabulary (C322.1). Every non-empty needs includes v2.
const (
	CapV2       = "v2"
	CapEars     = "ears"
	CapAnalyze  = "analyze"
	CapPlan     = "plan"
	CapAttach   = "attach"
	CapFallback = "fallback"
	CapAgent    = "agent"
)

// Limits of the spec (C321, C323.3).
const (
	MaxSeeds       = 16
	MaxAttachments = 16
)

// Spec is spec_json. The zero value is the plain render: `{}`.
type Spec struct {
	Inputs *Inputs     `json:"inputs,omitempty"`
	Ears   *Ears       `json:"ears,omitempty"`
	Plan   *Plan       `json:"plan,omitempty"`
	Attach []AttachRef `json:"attach,omitempty"`
}

// Inputs are the audio objects the server signs a fetch link for at submit; the link itself is never stored.
type Inputs struct {
	SrcAudio *Pin `json:"src_audio,omitempty"`
}

// Pin names an audio object by track and the digest of the master it was estimated against; a changed master fails the attempt
// before submit (jobs.ErrInputChanged).
type Pin struct {
	TrackID      string `json:"track_id"`
	MasterSHA256 string `json:"master_sha256"`
	Bytes        int64  `json:"bytes"`
}

// Ears configures the listening stage of an analyze job or the Tier B post-stage of a render.
type Ears struct {
	Lang       string   `json:"lang,omitempty"`
	BudgetS    int      `json:"budget_s,omitempty"`
	Stages     []string `json:"stages,omitempty"`
	KeepVocals bool     `json:"keep_vocals,omitempty"`
}

// Plan configures a plan job: one worker run per seed.
type Plan struct {
	Seeds          []int64 `json:"seeds"`
	SecondsPerSeed int     `json:"seconds_per_seed"`
}

// AttachRef is a small file the worker receives next to the request. Source names where the server reads the bytes at submit,
// for example plan:<id>/score.abc; the Desk defines its sources.
type AttachRef struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Source string `json:"source"`
}

var earsStages = []string{"separate", "asr", "transcribe"}

// Parse reads a spec_json document strictly: unknown fields, trailing data and out-of-range values are refused. An empty
// document and null are the zero spec.
func Parse(raw []byte) (Spec, error) {
	var s Spec
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return s, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("jobspec: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Spec{}, errors.New("jobspec: trailing data")
	}
	if err := s.validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s Spec) validate() error {
	if s.Inputs != nil {
		p := s.Inputs.SrcAudio
		switch {
		case p == nil:
			return errors.New("jobspec: inputs names no audio")
		case p.TrackID == "":
			return errors.New("jobspec: input pin has no track_id")
		case !isHex64(p.MasterSHA256):
			return errors.New("jobspec: input pin master_sha256 is not a lowercase sha256")
		case p.Bytes <= 0:
			return errors.New("jobspec: input pin bytes must be positive")
		}
	}
	if e := s.Ears; e != nil {
		if e.BudgetS < 0 {
			return errors.New("jobspec: ears budget_s is negative")
		}
		for _, st := range e.Stages {
			if !slices.Contains(earsStages, st) {
				return fmt.Errorf("jobspec: ears stage %q is not one of %s", st, strings.Join(earsStages, ", "))
			}
		}
	}
	if p := s.Plan; p != nil {
		if n := len(p.Seeds); n < 1 || n > MaxSeeds {
			return fmt.Errorf("jobspec: plan needs 1 to %d seeds", MaxSeeds)
		}
		seen := map[int64]bool{}
		for _, sd := range p.Seeds {
			if seen[sd] {
				return errors.New("jobspec: plan seeds repeat")
			}
			seen[sd] = true
		}
		if p.SecondsPerSeed <= 0 {
			return errors.New("jobspec: plan seconds_per_seed must be positive")
		}
	}
	if len(s.Attach) > MaxAttachments {
		return fmt.Errorf("jobspec: at most %d attachments", MaxAttachments)
	}
	names := map[string]bool{}
	for _, a := range s.Attach {
		switch {
		case a.Name == "" || a.Source == "":
			return errors.New("jobspec: attachment needs a name and a source")
		case !isHex64(a.SHA256):
			return errors.New("jobspec: attachment sha256 is not a lowercase sha256")
		case names[a.Name]:
			return errors.New("jobspec: attachment names repeat")
		}
		names[a.Name] = true
	}
	return nil
}

// Canonical is the deterministic JSON of the spec: what jobs.spec_json holds.
func (s Spec) Canonical() ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return canonjson.Canonical(b)
}

// Needs is the sorted capability set a job of kind with spec requires of its worker. A plain render needs nothing (it stays on
// mm.worker/1, byte for byte as before); anything else needs v2 as well (C322.1). The slice is never nil.
func Needs(kind string, s Spec) []string {
	var n []string
	switch kind {
	case KindAnalyze:
		n = append(n, CapAnalyze)
	case KindPlan:
		n = append(n, CapPlan)
	}
	if s.Ears != nil {
		n = append(n, CapEars)
	}
	if len(s.Attach) > 0 {
		n = append(n, CapAttach)
	}
	return normalize(n)
}

// WithCaps adds capabilities a routed backend requires to frozen needs (bulk A2), keeping the canonical order. The input is not
// modified.
func WithCaps(needs []string, caps ...string) []string {
	return normalize(append(slices.Clone(needs), caps...))
}

// normalize sorts and deduplicates, drops blanks, and adds v2 to a non-empty set.
func normalize(n []string) []string {
	out := make([]string, 0, len(n)+1)
	for _, c := range n {
		if c != "" {
			out = append(out, c)
		}
	}
	if len(out) > 0 {
		out = append(out, CapV2)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ParseNeeds reads a needs_json array.
func ParseNeeds(raw string) ([]string, error) {
	var n []string
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&n); err != nil {
		return nil, fmt.Errorf("jobspec: needs: %w", err)
	}
	if n == nil {
		return nil, errors.New("jobspec: needs: not a JSON array")
	}
	if dec.More() {
		return nil, errors.New("jobspec: needs: trailing data")
	}
	return n, nil
}
