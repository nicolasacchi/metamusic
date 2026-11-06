package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/ops"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// The render child (C210.4, D-T1-7): `metamusic dsp-run --work <abs dir>`. The supervisor (internal/transform) writes job.json and
// the decoded inputs into the work dir, starts this process with an explicit minimal environment, and reads progress lines from
// stdout and report.json and out.pcm24 afterwards. This file imports only the standard library and internal/dsp/...
// (TestDspRun_Imports): the child knows nothing of the server, the database or the network.

// Exit codes of the child (transform.ExitOK ... ExitInternal).
const (
	dspExitInvalid  = 10 // invalid job
	dspExitBudget   = 11 // work budget exceeded
	dspExitNonFinit = 12 // non-finite or silent output
	dspExitInternal = 13
)

// The fixed names of a work dir; the child opens nothing else, and only through os.Root.
const (
	dspJobFile    = "job.json"
	dspOutFile    = "out.pcm24"
	dspReportFile = "report.json"
)

const (
	maxJobBytes   = 4 << 20
	maxInputs     = 2
	minRate       = 8000
	maxRate       = 384000
	maxInputFrame = 1 << 28 // 2^28 frames: 1.5 hours at 48 kHz, far above MaxInputS, and 2 GiB of float32
)

var inputName = regexp.MustCompile(`^in-[0-9]{1,3}\.f32$`)

// dspJob is job.json as the child reads it. Unknown members are ignored so the bridge can grow the document.
type dspJob struct {
	V          int             `json:"v"`
	Kind       string          `json:"kind"`
	Rate       int             `json:"rate"`
	Seed       int64           `json:"seed"`
	Params     json.RawMessage `json:"params"`
	Alignment  json.RawMessage `json:"alignment"`
	Inputs     []dspJobInput   `json:"inputs"`
	UnitsMax   int64           `json:"units_max"`
	TargetLUFS float64         `json:"target_lufs"`
	RefLUFS    *float64        `json:"ref_lufs"`
	Out        string          `json:"out"`
	Report     string          `json:"report"`
}

type dspJobInput struct {
	Role        string   `json:"role"`
	File        string   `json:"file"`
	Frames      int64    `json:"frames"`
	BPM         *float64 `json:"bpm"`
	BeatsPerBar *int     `json:"beats_per_bar"`
	KeyShiftS   float64  `json:"key_shift_s"`
	DownbeatS   *float64 `json:"downbeat_s"`
}

// dspFailure is a run that ends with one of the child's exit codes.
type dspFailure struct {
	code int
	msg  string
}

func (f *dspFailure) Error() string { return f.msg }

func dspFail(code int, format string, a ...any) *dspFailure {
	return &dspFailure{code: code, msg: fmt.Sprintf(format, a...)}
}

// cmdDspRun is `metamusic dsp-run --work <abs dir>`.
func cmdDspRun(ctx context.Context, args []string, s streams) error {
	fs := flag.NewFlagSet("dsp-run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	work := fs.String("work", "", "absolute work directory")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError("usage: metamusic dsp-run --work <absolute dir>")
	}
	if !filepath.IsAbs(*work) {
		return usageError("dsp-run: --work must be an absolute directory")
	}
	if err := dspRun(ctx, *work, s.out); err != nil {
		var f *dspFailure
		if errors.As(err, &f) {
			return exitError{f.code, fmt.Errorf("dsp-run: %s", f.msg)}
		}
		return exitError{dspExitInternal, fmt.Errorf("dsp-run: %w", err)}
	}
	return nil
}

// dspRun reads job.json from dir, renders, and writes out.pcm24 then report.json. Progress lines go to progress. It returns a
// *dspFailure for the failures that have their own exit code; any other error is an internal failure (13).
func dspRun(ctx context.Context, dir string, progress io.Writer) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return dspFail(dspExitInvalid, "the work directory cannot be opened")
	}
	defer func() { _ = root.Close() }()
	job, err := readDspJob(root)
	if err != nil {
		return err
	}
	inputs, err := readDspInputs(root, job)
	if err != nil {
		return err
	}
	last := -1
	emit := func(p int, phase string) {
		if p > last {
			last = p
			_, _ = fmt.Fprintf(progress, "{\"p\":%d,\"phase\":%q}\n", p, phase)
		}
	}
	emit(0, "rendering")
	j := ops.Job{Kind: job.Kind, Rate: job.Rate, Seed: job.Seed, Params: job.Params, Alignment: job.Alignment, Inputs: inputs,
		TargetLUFS: job.TargetLUFS, Meter: &dsp.Meter{Max: job.UnitsMax}, Progress: emit}
	if job.RefLUFS != nil {
		j.RefLUFS = *job.RefLUFS
	}
	j.Scratch = &rootScratch{root: root}
	f, err := root.OpenFile(dspOutFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("the output cannot be created")
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	rep, rerr := ops.RunTo(ctx, j, bw)
	if rerr == nil {
		rerr = bw.Flush()
	}
	if rerr == nil {
		rerr = f.Sync()
	}
	if cerr := f.Close(); rerr == nil {
		rerr = cerr
	}
	if rerr != nil {
		_ = root.Remove(dspOutFile) // a render that fails leaves no output behind
		return mapRunError(rerr)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("the report cannot be encoded")
	}
	if err := writeRootFile(root, dspReportFile, raw); err != nil {
		return fmt.Errorf("the report cannot be written")
	}
	emit(1000, "finishing")
	return nil
}

// rootScratch is the scratch storage of the finishing stage: spool files with fixed-pattern names inside the work dir, opened
// through the same os.Root as everything else. The work dir is the supervisor's to remove, so a crash leaks nothing.
type rootScratch struct {
	root *os.Root
	n    int
}

type rootSpool struct {
	root *os.Root
	name string
	w    *os.File
}

func (s *rootScratch) NewSpool() (safety.Spool, error) {
	s.n++
	name := fmt.Sprintf("spool-%d.bin", s.n)
	w, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("a scratch file cannot be created")
	}
	return &rootSpool{root: s.root, name: name, w: w}, nil
}

func (p *rootSpool) Write(b []byte) (int, error) { return p.w.Write(b) }

func (p *rootSpool) Open() (io.ReadCloser, error) {
	if p.w != nil { // the first read ends the writing
		if err := p.w.Close(); err != nil {
			return nil, err
		}
		p.w = nil
	}
	f, err := p.root.Open(p.name)
	if err != nil {
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{bufio.NewReaderSize(f, 1<<20), f}, nil
}

func (p *rootSpool) Close() error {
	if p.w != nil {
		_ = p.w.Close()
		p.w = nil
	}
	return p.root.Remove(p.name)
}

// mapRunError turns an operator or dispatcher error into the child's exit code.
func mapRunError(err error) error {
	var pe *ops.ParamError
	switch {
	case errors.As(err, &pe):
		return dspFail(dspExitInvalid, "invalid job: %s: %s", pe.Field, pe.Rule)
	case errors.Is(err, dsp.ErrBudget):
		return dspFail(dspExitBudget, "the work budget is exceeded")
	case errors.Is(err, safety.ErrNonFinite):
		return dspFail(dspExitNonFinit, "the render produced a non-finite sample")
	case errors.Is(err, safety.ErrSilent):
		return dspFail(dspExitNonFinit, "the render is silent")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return dspFail(dspExitInternal, "interrupted")
	}
	return dspFail(dspExitInternal, "render failed: %v", err)
}

func writeRootFile(root *os.Root, name string, b []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// readDspJob reads and validates job.json.
func readDspJob(root *os.Root) (dspJob, error) {
	var job dspJob
	f, err := root.Open(dspJobFile)
	if err != nil {
		return job, dspFail(dspExitInvalid, "job.json cannot be opened")
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxJobBytes+1))
	if err != nil || len(raw) > maxJobBytes {
		return job, dspFail(dspExitInvalid, "job.json cannot be read")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&job); err != nil || dec.More() {
		return job, dspFail(dspExitInvalid, "job.json is not a valid job document")
	}
	switch {
	case job.V != 1:
		return job, dspFail(dspExitInvalid, "job.json: unsupported version")
	case job.Kind == "":
		return job, dspFail(dspExitInvalid, "job.json: no kind")
	case job.Rate < minRate || job.Rate > maxRate:
		return job, dspFail(dspExitInvalid, "job.json: rate out of range")
	case len(job.Inputs) < 1 || len(job.Inputs) > maxInputs:
		return job, dspFail(dspExitInvalid, "job.json: the input count is out of range")
	case job.Out != dspOutFile || job.Report != dspReportFile:
		return job, dspFail(dspExitInvalid, "job.json: the output names are fixed")
	case job.UnitsMax < 0:
		return job, dspFail(dspExitInvalid, "job.json: negative work budget")
	}
	seen := map[string]bool{}
	for _, in := range job.Inputs {
		if !inputName.MatchString(in.File) || seen[in.File] {
			return job, dspFail(dspExitInvalid, "job.json: an input file name is not allowed")
		}
		seen[in.File] = true
		if in.Frames < 1 || in.Frames > maxInputFrame {
			return job, dspFail(dspExitInvalid, "job.json: an input length is out of range")
		}
	}
	return job, nil
}

// readDspInputs reads the interleaved stereo float32 inputs. The file must be exactly frames x 8 bytes.
func readDspInputs(root *os.Root, job dspJob) ([]ops.Input, error) {
	var out []ops.Input
	for _, in := range job.Inputs {
		f, err := root.Open(in.File)
		if err != nil {
			return nil, dspFail(dspExitInvalid, "an input file cannot be opened")
		}
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != in.Frames*8 {
			_ = f.Close()
			return nil, dspFail(dspExitInvalid, "an input file does not match its frame count")
		}
		b, err := pcm.ReadF32LE(bufio.NewReaderSize(f, 1<<20), job.Rate, 2, int(in.Frames))
		_ = f.Close()
		if err != nil {
			return nil, dspFail(dspExitInvalid, "an input file cannot be read")
		}
		x := ops.Input{Role: in.Role, Buf: b, KeyShiftS: in.KeyShiftS, DownbeatS: in.DownbeatS}
		if in.BPM != nil {
			x.BPM = *in.BPM
		}
		if in.BeatsPerBar != nil {
			x.BeatsPerBar = *in.BeatsPerBar
		}
		out = append(out, x)
	}
	return out, nil
}
