package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// Contract is the worker contract version carried by every input and output.
const Contract = "mm.worker/1"

// MaxRunBody is the largest /run body the app sends (C73.3); estimates refuse takes whose input would
// exceed it, so a submit never discovers the limit after reserving.
const MaxRunBody = 2 << 20

// WorkerInput is the decoded job input (C73.3).
type WorkerInput struct {
	Contract      string          `json:"contract"`
	TakeID        string          `json:"take_id"`
	AttemptID     string          `json:"attempt_id"`
	Engine        string          `json:"engine"`
	Request       json.RawMessage `json:"request"`
	TimeoutS      int             `json:"timeout_s"`
	UploadBudgetS int             `json:"upload_budget_s"`
	DeadlineUnix  int64           `json:"deadline_unix"`
	// MinAudioS is the forced minimum render length in seconds (X4); 0 is omitted. A worker image that predates
	// the field rejects the input (BAD_INPUT), so a forced minimum is never silently dropped.
	MinAudioS int                     `json:"min_audio_s,omitempty"`
	Start     StartTarget             `json:"start"`
	Uploads   map[string]UploadTarget `json:"uploads"`
	Expect    WorkerExpect            `json:"expect"`
	// Inputs are the fetchable source and reference audio of an ACE-Step edit (C153.2). Absent for yue2, whose job is
	// byte-identical to M3's (TestTakeSpec_Yue2InputByteIdentical).
	Inputs map[string]InputRef `json:"inputs,omitempty"`
}

// StartTarget is the signed start-handshake URL (D-M3-28).
type StartTarget struct {
	URL string `json:"url"`
}

var (
	idRE     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	engineRE = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	urlRE    = regexp.MustCompile(`^https?://[^\s]+$`)
	revRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

const (
	maxURLLen   = 2048
	maxTimeoutS = 2700
	// MaxMinAudioS is the largest forced minimum: yue2's semantic max_tokens is 9000 = 360 s at 25 tokens/s.
	MaxMinAudioS = 360
)

// ErrWorkerInput is wrapped by every ParseWorkerInput error. Messages name fields, never values.
var ErrWorkerInput = errors.New("provider: invalid worker input")

// ParseWorkerInput is the hand-written production validator of mm-worker-1.input.schema.json: it accepts
// exactly the documents the schema accepts (TestParseWorkerInput_MatchesSchema checks both over a table
// with the test-only jsonschema-go). It decodes strictly: unknown keys at any level are errors.
func ParseWorkerInput(raw []byte) (WorkerInput, error) {
	var in WorkerInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return WorkerInput{}, fmt.Errorf("%w: %s", ErrWorkerInput, shapeErr(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return WorkerInput{}, fmt.Errorf("%w: trailing data", ErrWorkerInput)
	}
	bad := func(field, rule string) (WorkerInput, error) {
		return WorkerInput{}, fmt.Errorf("%w: %s %s", ErrWorkerInput, field, rule)
	}
	var top map[string]json.RawMessage // encoding/json reads null into an int as "absent"; the schema forbids it
	if json.Unmarshal(raw, &top) == nil && bytes.Equal(bytes.TrimSpace(top["min_audio_s"]), []byte("null")) {
		return bad("min_audio_s", "must be an integer")
	}
	switch {
	case in.Contract != Contract:
		return bad("contract", "must be "+Contract)
	case !idRE.MatchString(in.TakeID):
		return bad("take_id", "malformed")
	case !idRE.MatchString(in.AttemptID):
		return bad("attempt_id", "malformed")
	case !engineRE.MatchString(in.Engine):
		return bad("engine", "malformed")
	case !isNonEmptyObject(in.Request):
		return bad("request", "must be a non-empty object")
	case in.TimeoutS < 1 || in.TimeoutS > maxTimeoutS:
		return bad("timeout_s", "out of range")
	case in.UploadBudgetS < 1 || in.UploadBudgetS > maxTimeoutS:
		return bad("upload_budget_s", "out of range")
	case in.DeadlineUnix < 1:
		return bad("deadline_unix", "out of range")
	case in.MinAudioS < 0 || in.MinAudioS > MaxMinAudioS:
		return bad("min_audio_s", "out of range")
	case in.Engine == EngineACEStep && top != nil && top["min_audio_s"] != nil:
		return bad("min_audio_s", "is not part of the acestep15 input")
	case in.Engine != EngineACEStep && in.Inputs != nil:
		return bad("inputs", "is only part of the acestep15 input")
	case !validURL(in.Start.URL):
		return bad("start.url", "malformed")
	}
	engine := in.Engine
	if engine != EngineACEStep {
		engine = EngineYuE2
	}
	list, _ := ArtifactsFor(engine)
	for _, a := range list {
		if _, ok := in.Uploads[a.Name]; a.Required && !ok {
			return bad("uploads."+a.Name, "missing")
		}
	}
	for name, t := range in.Uploads {
		limit, ok := ArtifactMaxBytesFor(engine, name)
		switch {
		case !ok:
			return bad("uploads."+name, "is not an allowlisted artifact")
		case !validURL(t.URL):
			return bad("uploads."+name+".url", "malformed")
		case t.MaxBytes < 1 || t.MaxBytes > limit:
			return bad("uploads."+name+".max_bytes", "out of range")
		}
	}
	if err := checkInputs(in); err != nil {
		return WorkerInput{}, err
	}
	if len(in.Expect.Weights) == 0 {
		return bad("expect.weights", "must not be empty")
	}
	if in.Engine == EngineACEStep && len(in.Expect.Weights) > 2 {
		return bad("expect.weights", "holds at most two repositories")
	}
	for repo, rev := range in.Expect.Weights {
		if !revRE.MatchString(rev) {
			return bad("expect.weights."+repo, "must be a 40-digit hex revision")
		}
	}
	return in, nil
}

// maxInputBytes bounds one fetchable input (a frozen master, at most 600 s of 24-bit stereo 48 kHz PCM).
const maxInputBytes = 192 << 20

var sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// checkInputs validates the ACE-Step inputs object: keys are src_audio and reference_audio, each needs a URL, a digest
// and a size, and the keys match the request (src iff src_audio, ref iff reference_audio).
func checkInputs(in WorkerInput) error {
	if in.Engine != EngineACEStep {
		return nil
	}
	bad := func(field, rule string) error { return fmt.Errorf("%w: %s %s", ErrWorkerInput, field, rule) }
	var req struct {
		Src json.RawMessage `json:"src"`
		Ref json.RawMessage `json:"ref"`
	}
	_ = json.Unmarshal(in.Request, &req)
	hasSrc, hasRef := len(req.Src) > 0 && string(req.Src) != "null", len(req.Ref) > 0 && string(req.Ref) != "null"
	for k, r := range in.Inputs {
		if k != InputSrcAudio && k != InputReferenceAudio {
			return bad("inputs."+k, "is not an input name")
		}
		switch {
		case !validURL(r.URL):
			return bad("inputs."+k+".url", "malformed")
		case !sha256RE.MatchString(r.SHA256):
			return bad("inputs."+k+".sha256", "must be 64 hex digits")
		case r.Bytes < 1 || r.Bytes > maxInputBytes:
			return bad("inputs."+k+".bytes", "out of range")
		}
	}
	if _, ok := in.Inputs[InputSrcAudio]; ok != hasSrc {
		return bad("inputs.src_audio", "must be present exactly when the request has src")
	}
	if _, ok := in.Inputs[InputReferenceAudio]; ok != hasRef {
		return bad("inputs.reference_audio", "must be present exactly when the request has ref")
	}
	return nil
}

func validURL(u string) bool { return len(u) <= maxURLLen && urlRE.MatchString(u) }

func isNonEmptyObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && len(m) > 0
}

// shapeErr reduces a decoding error to its kind and field, dropping any value text.
func shapeErr(err error) string {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return "wrong type for " + ute.Field
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return "not valid JSON"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "not valid JSON"
	}
	return "unknown or malformed field" // DisallowUnknownFields messages carry the key name only; keep it generic
}

// BuildWorkerInput renders the job input of a TakeSpec (C73.3). Backends call it; the result always passes
// ParseWorkerInput for a well-formed TakeSpec.
func BuildWorkerInput(t TakeSpec) ([]byte, error) {
	uploads := make(map[string]UploadTarget, len(t.Uploads))
	for k, v := range t.Uploads {
		uploads[k] = v
	}
	return json.Marshal(WorkerInput{
		Contract: Contract, TakeID: t.JobID, AttemptID: t.AttemptID, Engine: t.Engine, Request: t.Request,
		TimeoutS: t.TimeoutS, UploadBudgetS: t.UploadBudgetS, DeadlineUnix: t.Deadline.Unix(), MinAudioS: t.MinAudioS,
		Start: StartTarget{URL: t.StartURL}, Uploads: uploads, Expect: t.Expect, Inputs: t.Inputs,
	})
}

// DecodeOutput strictly decodes a worker output (C73.5): unknown keys are errors.
func DecodeOutput(raw []byte) (Output, error) {
	var o Output
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&o); err != nil {
		return Output{}, fmt.Errorf("%w: output: %s", ErrWorkerInput, shapeErr(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Output{}, fmt.Errorf("%w: output: trailing data", ErrWorkerInput)
	}
	return o, nil
}
