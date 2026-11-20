package tierb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// ErrContract is wrapped by every Parse failure.
var ErrContract = errors.New("tierb: analysis.json violates mm.ears-b/1")

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrContract, fmt.Sprintf(format, a...))
}

// node describes the required members of a JSON object: nullable members may be null, kids describe nested
// objects, elem the objects inside an array member. The same shape as mm-ears-b-1.schema.json; the agreement
// test keeps the two honest.
type node struct {
	keys     []string
	nullable map[string]bool
	kids     map[string]*node
	elem     map[string]*node
}

var (
	modelNode = &node{keys: []string{"name", "sha256"}}
	specRoot  = &node{
		keys:     []string{"contract", "status", "code", "stages", "models", "input", "timings", "resources", "asr", "pitch", "vocals", "errors"},
		nullable: map[string]bool{"code": true, "input": true, "asr": true, "pitch": true, "vocals": true},
		kids: map[string]*node{
			"stages": {keys: []string{"separate", "asr", "transcribe"}},
			"models": {
				keys:     []string{"separator", "asr", "pitch"},
				nullable: map[string]bool{"separator": true, "asr": true, "pitch": true},
				kids: map[string]*node{
					"separator": modelNode,
					"asr":       {keys: []string{"name", "sha256", "compute_type"}},
					"pitch":     modelNode,
				},
			},
			"input":     {keys: []string{"duration_s", "sample_rate", "channels", "codec"}},
			"timings":   {keys: []string{"load_s", "separate_s", "asr_s", "transcribe_s", "total_s"}},
			"resources": {keys: []string{"device", "peak_vram_mib", "parent_reserved_mib"}, nullable: map[string]bool{"parent_reserved_mib": true}},
			"asr": {
				keys: []string{"language", "forced", "segments", "words"},
				elem: map[string]*node{
					"segments": {keys: []string{"start", "end", "avg_logprob", "no_speech_p", "w0", "w1"}},
					"words":    {keys: []string{"w", "start", "end", "p"}},
				},
			},
			"pitch":  {keys: []string{"hop_s", "cents", "conf"}},
			"vocals": {keys: []string{"rms_dbfs", "active_s"}},
		},
		elem: map[string]*node{"errors": {keys: []string{"stage", "code", "message"}}},
	}
)

// check verifies that every required key is present and that only nullable ones are null.
func (n *node) check(v any, path string) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return bad("%s is not an object", path)
	}
	for _, k := range n.keys {
		m, present := obj[k]
		if !present {
			return bad("%s.%s is missing", path, k)
		}
		if m == nil {
			if !n.nullable[k] {
				return bad("%s.%s is null", path, k)
			}
			continue
		}
		if kid := n.kids[k]; kid != nil {
			if err := kid.check(m, path+"."+k); err != nil {
				return err
			}
		}
		if el := n.elem[k]; el != nil {
			arr, ok := m.([]any)
			if !ok {
				return bad("%s.%s is not an array", path, k)
			}
			for i, e := range arr {
				if err := el.check(e, fmt.Sprintf("%s.%s[%d]", path, k, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

var validCodes = map[string]bool{
	CodeModelsMissing: true, CodeInputDecode: true, CodeSeparationFailed: true, CodeASRFailed: true,
	CodeDeadline: true, CodeOOM: true, CodeSubprocessFailed: true,
}

func validStage(s string) bool {
	return s == StageOK || s == StageError || s == StageSkipped || s == StageNotRequested
}

// Parse is the strict parser of an analysis.json: unknown or missing members, wrong types, every limit of
// C332.2 and the status rules are refused. It never returns a partially filled Raw.
func Parse(data []byte) (Raw, error) {
	if len(data) > MaxDocBytes {
		return Raw{}, bad("document is %d bytes, over %d", len(data), MaxDocBytes)
	}
	var generic any
	if err := decodeStrict(data, &generic); err != nil {
		return Raw{}, bad("not valid JSON: %v", err)
	}
	if err := specRoot.check(generic, "$"); err != nil {
		return Raw{}, err
	}
	var r Raw
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Raw{}, bad("%v", err)
	}
	if err := validate(&r); err != nil {
		return Raw{}, err
	}
	return r, nil
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the document")
	}
	return nil
}

func validate(r *Raw) error {
	if r.Contract != ContractID {
		return bad("contract is %q", r.Contract)
	}
	if r.Status != StatusOK && r.Status != StatusError {
		return bad("status is %q", r.Status)
	}
	for name, s := range map[string]string{"separate": r.Stages.Separate, "asr": r.Stages.ASR, "transcribe": r.Stages.Transcribe} {
		if !validStage(s) {
			return bad("stages.%s is %q", name, s)
		}
	}
	// status ok iff asr ok (C332.2)
	if (r.Status == StatusOK) != (r.Stages.ASR == StageOK) {
		return bad("status %q with stages.asr %q", r.Status, r.Stages.ASR)
	}
	if r.Status == StatusOK && r.Code != nil {
		return bad("code must be null when status is ok")
	}
	if r.Status == StatusError && (r.Code == nil || !validCodes[*r.Code]) {
		return bad("status error needs one of the documented codes")
	}
	if r.Status == StatusOK && r.ASR == nil {
		return bad("status ok needs asr")
	}
	if (r.Stages.Transcribe == StageOK) != (r.Pitch != nil) {
		return bad("pitch must be present exactly when stages.transcribe is ok")
	}
	for _, m := range []*Model{r.Models.Separator, r.Models.Pitch} {
		if m != nil && !sha256Re.MatchString(m.SHA256) {
			return bad("model sha256 is not 64 lowercase hex digits")
		}
	}
	if m := r.Models.ASR; m != nil && !sha256Re.MatchString(m.SHA256) {
		return bad("asr model sha256 is not 64 lowercase hex digits")
	}
	if r.Input != nil && (r.Input.DurationS < 0 || r.Input.SampleRate < 1 || r.Input.Channels < 1) {
		return bad("input out of range")
	}
	if len(r.Errors) > MaxErrors {
		return bad("%d errors, over %d", len(r.Errors), MaxErrors)
	}
	for i, e := range r.Errors {
		if !validCodes[e.Code] || len(e.Message) > 300 {
			return bad("errors[%d] invalid", i)
		}
		switch e.Stage {
		case "load", "input", "separate", "asr", "transcribe":
		default:
			return bad("errors[%d].stage is %q", i, e.Stage)
		}
	}
	if err := validateASR(r.ASR); err != nil {
		return err
	}
	if err := validatePitch(r.Pitch); err != nil {
		return err
	}
	if v := r.Vocals; v != nil && (v.RMSdBFS > 0 || v.ActiveS < 0) {
		return bad("vocals out of range")
	}
	return nil
}

func validateASR(a *ASR) error {
	if a == nil {
		return nil
	}
	if len(a.Language) < 2 || len(a.Language) > 8 {
		return bad("asr.language %q", a.Language)
	}
	if len(a.Words) > MaxWords {
		return bad("%d words, over %d", len(a.Words), MaxWords)
	}
	if len(a.Segments) > MaxSegments {
		return bad("%d segments, over %d", len(a.Segments), MaxSegments)
	}
	for i, w := range a.Words {
		if w.Start < 0 || w.End < w.Start || w.P < 0 || w.P > 1 || len(w.W) > 64 {
			return bad("words[%d] out of range", i)
		}
	}
	for i, s := range a.Segments {
		if s.Start < 0 || s.End < s.Start || s.AvgLogprob > 0 || s.NoSpeechP < 0 || s.NoSpeechP > 1 {
			return bad("segments[%d] out of range", i)
		}
		if s.W0 < 0 || s.W0 > s.W1 || s.W1 > len(a.Words) {
			return bad("segments[%d] word range [%d,%d) outside %d words", i, s.W0, s.W1, len(a.Words))
		}
		for j := s.W0 + 1; j < s.W1; j++ {
			if a.Words[j].Start < a.Words[j-1].Start {
				return bad("segments[%d]: word times decrease at word %d", i, j)
			}
		}
	}
	return nil
}

func validatePitch(p *Pitch) error {
	if p == nil {
		return nil
	}
	if p.HopS != PitchHopS {
		return bad("pitch.hop_s is %v", p.HopS)
	}
	if len(p.Cents) != len(p.Conf) {
		return bad("pitch.cents has %d frames, conf %d", len(p.Cents), len(p.Conf))
	}
	if len(p.Cents) > MaxPitchFrames {
		return bad("%d pitch frames, over %d", len(p.Cents), MaxPitchFrames)
	}
	for i := range p.Cents {
		if p.Cents[i] < 0 || p.Conf[i] < 0 || p.Conf[i] > 100 {
			return bad("pitch frame %d out of range", i)
		}
	}
	return nil
}
