package runpod

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MeasuredSchema is the format of engine-canary-measured.json (C155.4): what the ACE-Step canary observed, redacted
// (durations, sizes, RTF, load seconds, billed against reserved, artifact names, peak VRAM). The canary never writes the
// committed file by itself; `runpod canary --measured-out FILE` writes the merged document and the operator commits it
// in a reviewed PR (E2-X4).
const MeasuredSchema = "mm.engine-canary-measured/1"

// EmbeddedMeasured is the committed engine-canary-measured.json.
//
//go:embed engine-canary-measured.json
var EmbeddedMeasured []byte

// MeasuredFile is engine-canary-measured.json.
type MeasuredFile struct {
	Schema string                  `json:"schema"`
	Engine string                  `json:"engine"`
	Cases  map[string]MeasuredCase `json:"cases"`
}

// MeasuredCase is the latest run of one case.
type MeasuredCase struct {
	RecordedOn string         `json:"recorded_on"`
	Outcome    string         `json:"outcome"`
	Checks     []CanaryCheck  `json:"checks,omitempty"`
	Stages     []CanaryStage  `json:"stages,omitempty"`
	Measured   map[string]any `json:"measured,omitempty"`
}

// ValidateMeasured checks the structure of engine-canary-measured.json.
func ValidateMeasured(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f MeasuredFile
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("runpod: engine-canary-measured.json: %w", errors.Unwrap(err))
	}
	if f.Schema != MeasuredSchema || f.Engine != EngineACEStep {
		return errors.New("runpod: engine-canary-measured.json: unknown schema or engine")
	}
	for name, c := range f.Cases {
		if _, ok := aceCaseByName(CanaryCase(name)); !ok {
			return fmt.Errorf("runpod: engine-canary-measured.json: unknown case %q", name)
		}
		if _, err := time.Parse("2006-01-02", c.RecordedOn); err != nil {
			return fmt.Errorf("runpod: engine-canary-measured.json: case %s has no recorded_on date", name)
		}
	}
	return CheckFileSafe(raw, "")
}

// MergeMeasured records a report of an ACE-Step case into the measured document (existing may be nil) and returns the
// new bytes. A refused or aborted run measured nothing and is not recorded. The result passes CheckFileSafe: no key,
// token, URL or long identifier can be in it.
func MergeMeasured(existing []byte, rep *CanaryReport, day time.Time) ([]byte, error) {
	if rep == nil || rep.Engine != EngineACEStep {
		return nil, errors.New("runpod: only an acestep15 report is recorded")
	}
	f := MeasuredFile{Schema: MeasuredSchema, Engine: EngineACEStep, Cases: map[string]MeasuredCase{}}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := ValidateMeasured(existing); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(existing, &f); err != nil {
			return nil, err
		}
	}
	if rep.Outcome == "pass" || rep.Outcome == "fail" {
		stages := make([]CanaryStage, len(rep.Stages))
		for i, s := range rep.Stages {
			s.JobID, s.BatchID = "", ""
			stages[i] = s
		}
		f.Cases[string(rep.Case)] = MeasuredCase{RecordedOn: day.UTC().Format("2006-01-02"), Outcome: rep.Outcome, Checks: rep.Checks,
			Stages: stages, Measured: rep.Measured}
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if err := CheckFileSafe(raw, ""); err != nil {
		return nil, err
	}
	return raw, nil
}
