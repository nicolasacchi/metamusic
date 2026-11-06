package transform

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The ingest bridge (C210.6): the nine steps of one attempt. The result is an ordinary Take written through the library's one
// write path (origin transform, stored as origin job with import_rule 'transform', no job id, 24-bit stereo, transform_id = the
// take id), frozen by the M1 pipeline like every master (mode 0444, tags written once).

// Ingester is the M1 freeze (*ingest.Pipeline).
type Ingester interface {
	Ingest(ctx context.Context, req ingest.Request) (ingest.Result, error)
	Discard(r ingest.Result) error
}

// Ears is what the bridge needs of the Ears service: Tier A after ingest and the parent's measured loudness.
type Ears interface {
	AnalyzeTrack(ctx context.Context, trackID string, o ears.AnalyzeOptions) (ears.Outcome, error)
	Perception(ctx context.Context, trackID string) (ears.Perception, error)
}

// Resampler converts a stereo buffer to another rate (D-T1-19: used only for a second input at a different rate; the production
// value is dsp.Resample once D1 lands).
type Resampler func(b pcm.Buffer, to int) (pcm.Buffer, error)

// Fault points (tests).
const (
	FaultAfterDecode = "after_decode"
	FaultAfterChild  = "after_child"
	FaultAfterEncode = "after_encode"
	FaultAfterIngest = "after_ingest"
)

// cpuAcct sums the CPU an attempt used: the child's user+system time plus the wall time of the tool runs (C210.4).
type cpuAcct struct{ d time.Duration }

func (a *cpuAcct) add(d time.Duration) { a.d += d }
func (a *cpuAcct) ms() int64           { return a.d.Milliseconds() }

func fail(code, format string, a ...any) *runFailure {
	return &runFailure{code: code, msg: fmt.Sprintf(format, a...)}
}

// crash is the failure Options.Fault produces: the attempt ends without any cleanup or row write, as a dead process would.
func crash(err error) *runFailure { return &runFailure{code: "FAULT", msg: err.Error(), crash: true} }

func (s *Service) fault(point string) *runFailure {
	if s.o.Fault == nil {
		return nil
	}
	if err := s.o.Fault(point); err != nil {
		return crash(err)
	}
	return nil
}

func workName(id string) string { return "transform-" + id }

func (s *Service) work(id string) (rel, abs string) {
	rel = path.Join("incoming", workName(id))
	return rel, filepath.Join(s.o.DataDir, filepath.FromSlash(rel))
}

func (s *Service) removeWork(id string) {
	rel, _ := s.work(id)
	_ = s.o.Data.RemoveAll(rel)
}

func (s *Service) media() Media {
	if s.o.Media != nil {
		return s.o.Media
	}
	return ToolMedia{Tools: s.o.Tools}
}

func (s *Service) child() ChildRunner {
	if s.o.Child != nil {
		return s.o.Child
	}
	return &ExecChild{Self: s.o.Self}
}

// recipeRow is the spec of a transform as the recipe stores it.
type recipeRow struct {
	requestJSON, requestID string
	seed                   int64
}

// inputRow is one input Take as an attempt reads it.
type inputRow struct {
	ref            InputRef
	ord            int
	masterPath     string
	rate, channels int
	totalSamples   int64
	rootID         string
	generation     int
	resampledFrom  int // 0 = none
	frames         int64
	title          string
	audio          bool // false for a score_source annotation
}

// staged is a work dir ready for the child: inputs decoded, job.json written.
type staged struct {
	spec      Spec
	rec       recipeRow
	inputs    []inputRow
	rel, abs  string
	inSeconds float64
	rate      int
}

// attempt runs steps 1-9 of C210.6 for a claimed row. It returns nil when the Take was committed, else the failure.
func (s *Service) attempt(ctx context.Context, row tRow, acct *cpuAcct) *runFailure {
	if row.Kind == KindScoreRender {
		return fail(ErrTransformFailed, "score_render needs the score toolchain, which is not part of this build")
	}
	st, f := s.stage(ctx, row, workName(row.ID), true, acct)
	if f != nil {
		return f
	}
	if f := s.fault(FaultAfterDecode); f != nil {
		return f
	}
	s.setProgress(ctx, row.ID, 0, "rendering")
	if f := s.render(ctx, row, st, acct, func(permille int, phase string) { s.setProgress(ctx, row.ID, permille, phase) }); f != nil {
		return f
	}
	if f := s.fault(FaultAfterChild); f != nil {
		return f
	}
	rep, f := s.readReport(st)
	if f != nil {
		return f
	}
	if ctx.Err() != nil {
		return fail(ErrInterrupted, "interrupted")
	}
	s.setPhase(ctx, row.ID, "encoding")
	d, err := s.media().Encode(ctx, filepath.Join(st.abs, outFile), filepath.Join(st.abs, flacFile), st.rate)
	acct.add(d)
	if err != nil {
		return fail(ErrTransformFailed, "encode failed: %v", err)
	}
	if f := s.fault(FaultAfterEncode); f != nil {
		return f
	}
	s.setPhase(ctx, row.ID, "ingesting")
	return s.ingestAndCommit(ctx, row, st, rep, acct)
}

// stage is steps 1 and 2: verify the inputs (and, for a real attempt, remove an orphan take), create the work dir, decode every
// input to in-<n>.f32 and write job.json. scratch is the work dir name under incoming/; live is false for Verify, which keeps
// no orphan handling and removes its scratch dir on failure.
func (s *Service) stage(ctx context.Context, row tRow, scratch string, live bool, acct *cpuAcct) (*staged, *runFailure) {
	st := &staged{rel: path.Join("incoming", scratch)}
	st.abs = filepath.Join(s.o.DataDir, filepath.FromSlash(st.rel))
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT request_json, request_id, seed FROM recipes WHERE id = ?`, row.RecipeID).
		Scan(&st.rec.requestJSON, &st.rec.requestID, &st.rec.seed); err != nil {
		return nil, fail(ErrTransformFailed, "the recipe of the transform could not be read")
	}
	if err := json.Unmarshal([]byte(st.rec.requestJSON), &st.spec); err != nil {
		return nil, fail(ErrInvalidTransform, "the stored spec is malformed")
	}
	if st.spec.Kind != row.Kind {
		return nil, fail(ErrInvalidTransform, "the stored spec does not match the transform kind")
	}
	if f := s.loadInputs(ctx, row, st); f != nil {
		return nil, f
	}
	if live {
		if f := s.removeOrphan(ctx, row.ID); f != nil {
			return nil, f
		}
	}
	// the work dir: fresh, 0700 (a leftover of an earlier attempt is removed first)
	_ = s.o.Data.RemoveAll(st.rel)
	if err := s.o.Data.MkdirAll("incoming", 0o755); err != nil {
		return nil, fail(ErrTransformFailed, "the work directory could not be created")
	}
	if err := s.o.Data.Mkdir(st.rel, 0o700); err != nil {
		return nil, fail(ErrTransformFailed, "the work directory could not be created")
	}
	ok := false
	defer func() {
		if !ok && !live {
			_ = s.o.Data.RemoveAll(st.rel)
		}
	}()

	st.rate = st.spec.SampleRate
	var docs []jobInput
	n := 0
	for i := range st.inputs {
		in := &st.inputs[i]
		if !in.audio {
			continue
		}
		if st.rate == 0 {
			st.rate = in.rate
		}
		if f := s.decodeInput(ctx, st, in, n, acct); f != nil {
			return nil, f
		}
		st.inSeconds += float64(in.frames) / float64(st.rate)
		docs = append(docs, s.jobInputOf(st, *in, n))
		n++
	}
	if len(docs) == 0 || st.rate <= 0 {
		return nil, fail(ErrInvalidTransform, "the transform has no audio input")
	}
	var ref *float64
	if s.o.Ears != nil && len(st.inputs) > 0 {
		if p, err := s.o.Ears.Perception(ctx, st.inputs[0].ref.TrackID); err == nil && p.LUFSI != nil {
			v := *p.LUFSI
			ref = &v
		}
	}
	job := jobDoc{V: 1, Kind: row.Kind, Rate: st.rate, Seed: st.spec.Seed, Params: st.spec.Params, Inputs: docs,
		UnitsMax: unitsBudget(row.MaxCPUMS, st.inSeconds, row.OutSecondsEst), TargetLUFS: st.spec.TargetLUFS, RefLUFS: ref,
		Out: outFile, Report: reportFile}
	if st.spec.Alignment != nil {
		job.Alignment, _ = json.Marshal(st.spec.Alignment)
	}
	b, err := json.Marshal(job)
	if err != nil {
		return nil, fail(ErrTransformFailed, "job.json could not be built")
	}
	if err := s.o.Data.WriteFile(path.Join(st.rel, jobFile), b, 0o600); err != nil {
		return nil, fail(ErrTransformFailed, "job.json could not be written")
	}
	ok = true
	return st, nil
}

// jobInputOf builds the per-input entry of job.json from the frozen alignment (absent for the kinds that use none).
func (s *Service) jobInputOf(st *staged, in inputRow, n int) jobInput {
	j := jobInput{Role: in.ref.Role, File: inFile(n), Frames: in.frames}
	if a := st.spec.Alignment; a != nil && in.ord < len(a.Inputs) {
		ia := a.Inputs[in.ord]
		bpm, bpb := ia.BPM, ia.BeatsPerBar
		j.BPM, j.BeatsPerBar, j.DownbeatS = &bpm, &bpb, ia.DownbeatS
	}
	return j
}

// loadInputs reads transform_inputs and the tracks they name, and checks that every input still is what the spec froze
// (step 1: INPUT_CHANGED is a corruption alarm, masters are frozen).
func (s *Service) loadInputs(ctx context.Context, row tRow, st *staged) *runFailure {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT ord, role, track_id, master_sha256 FROM transform_inputs WHERE transform_id = ? ORDER BY ord`, row.ID)
	if err != nil {
		return fail(ErrTransformFailed, "the inputs of the transform could not be read")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var in inputRow
		if err := rows.Scan(&in.ord, &in.ref.Role, &in.ref.TrackID, &in.ref.MasterSHA256); err != nil {
			return fail(ErrTransformFailed, "the inputs of the transform could not be read")
		}
		in.audio = in.ref.Role != "score_source"
		st.inputs = append(st.inputs, in)
	}
	if err := rows.Err(); err != nil {
		return fail(ErrTransformFailed, "the inputs of the transform could not be read")
	}
	_ = rows.Close()
	for i := range st.inputs {
		in := &st.inputs[i]
		var sha string
		err := s.o.Store.R.QueryRowContext(ctx, `SELECT master_path, master_sha256, sample_rate, channels, total_samples, root_track_id, generation, title
			FROM tracks WHERE id = ?`, in.ref.TrackID).Scan(&in.masterPath, &sha, &in.rate, &in.channels, &in.totalSamples, &in.rootID, &in.generation, &in.title)
		if err != nil || sha != in.ref.MasterSHA256 {
			return fail(ErrInputChanged, "an input take is gone or its master changed since the transform was created")
		}
		if i < len(st.spec.Inputs) && st.spec.Inputs[i].MasterSHA256 != "" && st.spec.Inputs[i].MasterSHA256 != sha {
			return fail(ErrInputChanged, "an input take no longer matches the spec")
		}
		if in.audio {
			if _, err := s.o.Data.Stat(in.masterPath); err != nil {
				return fail(ErrInputChanged, "the master of an input take is missing")
			}
		}
	}
	return nil
}

// removeOrphan removes what a crash between ingest and commit left of this transform's take (the id is fixed, so M1 would
// refuse the next attempt's takes/<id>). A take that does have a tracks row is never touched.
func (s *Service) removeOrphan(ctx context.Context, id string) *runFailure {
	var n int
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM tracks WHERE id = ?`, id).Scan(&n); err != nil {
		return fail(ErrTransformFailed, "the library could not be read")
	}
	if n > 0 {
		return fail(ErrTransformFailed, "a take with this id already exists")
	}
	s.discardOrphan(id)
	return nil
}

// decodeInput is step 2 for one input: ffmpeg at the native rate and channel count, mono duplicated in Go, a rate that differs
// from the working rate resampled in Go. The result is in-<n>.f32, interleaved stereo float32.
func (s *Service) decodeInput(ctx context.Context, st *staged, in *inputRow, n int, acct *cpuAcct) *runFailure {
	if in.channels < 1 || in.channels > 2 {
		return fail(ErrInvalidTransform, "an input take has %d channels; transforms take mono or stereo", in.channels)
	}
	raw := fmt.Sprintf("in-%d.raw", n)
	src := filepath.Join(s.o.DataDir, filepath.FromSlash(in.masterPath))
	d, err := s.media().Decode(ctx, src, filepath.Join(st.abs, raw))
	acct.add(d)
	if err != nil {
		return fail(ErrTransformFailed, "decode failed: %v", err)
	}
	rawRel := path.Join(st.rel, raw)
	dstRel := path.Join(st.rel, inFile(n))
	fi, err := s.o.Data.Stat(rawRel)
	if err != nil {
		return fail(ErrTransformFailed, "decode produced no output")
	}
	frameBytes := int64(4 * in.channels)
	if fi.Size() == 0 || fi.Size()%frameBytes != 0 {
		return fail(ErrTransformFailed, "decode produced an incomplete stream")
	}
	in.frames = fi.Size() / frameBytes
	if in.rate != st.rate {
		in.resampledFrom = in.rate
		if s.o.Resample == nil {
			return fail(ErrTransformFailed, "an input at %d Hz needs the resampler, which is not available", in.rate)
		}
		b, err := s.readInterleaved(rawRel, in.channels, in.rate)
		if err != nil {
			return fail(ErrTransformFailed, "decode output could not be read")
		}
		rb, err := s.o.Resample(b, st.rate)
		if err != nil {
			return fail(ErrTransformFailed, "resample failed: %v", err)
		}
		in.frames = int64(rb.Frames())
		if err := s.writeInterleaved(dstRel, rb); err != nil {
			return fail(ErrTransformFailed, "resampled input could not be written")
		}
		_ = s.o.Data.Remove(rawRel)
		return nil
	}
	if in.totalSamples > 0 && in.frames != in.totalSamples {
		return fail(ErrInputChanged, "an input decoded to a different length than its library row")
	}
	if in.channels == 2 {
		if err := s.o.Data.Rename(rawRel, dstRel); err != nil {
			return fail(ErrTransformFailed, "decode output could not be placed")
		}
		return nil
	}
	if err := s.expandMono(rawRel, dstRel); err != nil {
		return fail(ErrTransformFailed, "mono input could not be expanded")
	}
	_ = s.o.Data.Remove(rawRel)
	return nil
}

// expandMono copies a mono float32 stream to dst with every sample written twice.
func (s *Service) expandMono(srcRel, dstRel string) error {
	src, err := s.o.Data.Open(srcRel)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := s.o.Data.OpenFile(dstRel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	r := bufio.NewReaderSize(src, 1<<20)
	w := bufio.NewWriterSize(dst, 1<<20)
	var in [4]byte
	var out [8]byte
	for {
		if _, err := io.ReadFull(r, in[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = dst.Close()
			return err
		}
		copy(out[:4], in[:])
		copy(out[4:], in[:])
		if _, err := w.Write(out[:]); err != nil {
			_ = dst.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

// readInterleaved loads a float32 stream of 1 or 2 channels into a planar buffer (mono duplicated).
func (s *Service) readInterleaved(rel string, channels, rate int) (pcm.Buffer, error) {
	raw, err := s.o.Data.ReadFile(rel)
	if err != nil {
		return pcm.Buffer{}, err
	}
	frames := len(raw) / (4 * channels)
	b := pcm.Buffer{Rate: rate}
	b.Ch[0], b.Ch[1] = make([]float64, frames), make([]float64, frames)
	for i := 0; i < frames; i++ {
		l := float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4*channels:])))
		r := l
		if channels == 2 {
			r = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*8+4:])))
		}
		b.Ch[0][i], b.Ch[1][i] = l, r
	}
	return b, nil
}

// writeInterleaved writes a planar buffer as interleaved stereo float32.
func (s *Service) writeInterleaved(rel string, b pcm.Buffer) error {
	out := make([]byte, 8*b.Frames())
	for i := 0; i < b.Frames(); i++ {
		binary.LittleEndian.PutUint32(out[i*8:], math.Float32bits(float32(b.Ch[0][i])))
		binary.LittleEndian.PutUint32(out[i*8+4:], math.Float32bits(float32(b.Ch[1][i])))
	}
	return s.o.Data.WriteFile(rel, out, 0o600)
}

// render is step 3: the child, with progress written through the owner predicate, and the failure mapping of C210.6.
func (s *Service) render(ctx context.Context, row tRow, st *staged, acct *cpuAcct, progress ChildProgress) *runFailure {
	res, err := s.child().Run(ctx, ChildJob{Dir: st.abs, MemMB: row.MemMBEst, Deadline: wallDeadline(row.MaxCPUMS)}, progress)
	acct.add(res.CPU)
	switch {
	case errors.Is(err, ErrChildDeadline):
		return fail(ErrTimeout, "the render passed its wall deadline and was stopped")
	case errors.Is(err, ErrChildMemory):
		return fail(ErrMemoryExceeded, "the render passed its memory guard and was stopped")
	case errors.Is(err, ErrChildSignaled):
		f := fail(ErrInterrupted, "the render child was stopped from outside")
		f.requeue = true
		return f
	case err != nil:
		if ctx.Err() != nil {
			return fail(ErrInterrupted, "interrupted")
		}
		return fail(ErrTransformFailed, "the render child could not run: %v", err)
	case res.ExitCode != ExitOK:
		msg := res.StderrTail
		if msg == "" {
			msg = fmt.Sprintf("the render child exited with status %d", res.ExitCode)
		}
		return fail(exitCode(res.ExitCode), "%s", msg)
	}
	return nil
}

// readReport reads report.json and checks the PCM against it: length is 6 bytes a frame (24-bit stereo) and the bytes hash to
// the report's pcm_sha256, the oracle of Verify.
func (s *Service) readReport(st *staged) (childReport, *runFailure) {
	var rep childReport
	b, err := s.o.Data.ReadFile(path.Join(st.rel, reportFile))
	if err != nil || json.Unmarshal(b, &rep) != nil {
		return rep, fail(ErrTransformFailed, "the render produced no readable report")
	}
	f, err := s.o.Data.Open(path.Join(st.rel, outFile))
	if err != nil {
		return rep, fail(ErrTransformFailed, "the render produced no output")
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return rep, fail(ErrTransformFailed, "the render output could not be read")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	switch {
	case rep.Frames <= 0 || n != 6*rep.Frames:
		return rep, fail(ErrTransformFailed, "the render output does not match its report")
	case len(rep.PCMSHA256) != 64 || sum != rep.PCMSHA256:
		return rep, fail(ErrTransformFailed, "the render output does not match its report")
	}
	return rep, nil
}

// toolchainDoc and the rest of transform.json (C210.5).
type tdInput struct {
	Role          string          `json:"role"`
	TrackID       string          `json:"track_id"`
	MasterSHA256  string          `json:"master_sha256"`
	ResampledFrom *int            `json:"resampled_from"`
	BeatPhase     json.RawMessage `json:"beat_phase"`
}

type synthDoc struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	SoundfontSHA string `json:"soundfont_sha256"`
}

type transformDoc struct {
	V            int             `json:"v"`
	DSPVersion   string          `json:"dsp_version"`
	Toolchain    Toolchain       `json:"toolchain"`
	Kind         Kind            `json:"kind"`
	SpecHash     string          `json:"spec_hash"`
	PCMSHA256    string          `json:"pcm_sha256"`
	Frames       int64           `json:"frames"`
	Rate         int             `json:"rate"`
	CPUMS        int64           `json:"cpu_ms"`
	Units        int64           `json:"units"`
	Inputs       []tdInput       `json:"inputs"`
	Safety       safetyDoc       `json:"safety"`
	Warnings     []reportWarning `json:"warnings"`
	Synth        *synthDoc       `json:"synth"`
	Voices       json.RawMessage `json:"voices"`
	ToolVersions ToolVersions    `json:"tool_versions"`
	BlindSpots   []string        `json:"blind_spots"`
}

func (s *Service) transformDoc(ctx context.Context, row tRow, st *staged, rep childReport, acct *cpuAcct) transformDoc {
	d := transformDoc{V: 1, DSPVersion: st.spec.DSPVersion, Toolchain: s.toolchain(), Kind: row.Kind, SpecHash: row.SpecHash,
		PCMSHA256: rep.PCMSHA256, Frames: rep.Frames, Rate: st.rate, CPUMS: acct.ms(), Units: rep.Units, Safety: rep.Safety,
		Warnings: rep.Warnings, Voices: rep.Voices, ToolVersions: s.media().Versions(ctx), BlindSpots: rep.BlindSpots}
	if d.DSPVersion == "" {
		d.DSPVersion = DSPVersion
	}
	if d.Warnings == nil {
		d.Warnings = []reportWarning{}
	}
	if len(d.BlindSpots) == 0 {
		d.BlindSpots = []string{}
		if ks, ok := LookupKind(row.Kind); ok {
			d.BlindSpots = append(d.BlindSpots, ks.Capabilities().BlindSpots...)
		}
	}
	if sy := st.spec.Synth; sy != nil {
		d.Synth = &synthDoc{Name: sy.Name, Version: sy.Engine, SoundfontSHA: sy.SoundfontSHA}
	}
	n := 0
	for _, in := range st.inputs {
		ti := tdInput{Role: in.ref.Role, TrackID: in.ref.TrackID, MasterSHA256: in.ref.MasterSHA256, BeatPhase: json.RawMessage("null")}
		if in.resampledFrom > 0 {
			v := in.resampledFrom
			ti.ResampledFrom = &v
		}
		if in.audio {
			if n < len(rep.BeatPhase) && len(rep.BeatPhase[n]) > 0 {
				ti.BeatPhase = rep.BeatPhase[n]
			}
			n++
		}
		d.Inputs = append(d.Inputs, ti)
	}
	return d
}

// fileArtifact opens a work file as an ingest artifact.
func (s *Service) fileArtifact(rel, name string, opened *[]fs.File) (ingest.ArtifactSrc, error) {
	f, err := s.o.Data.Open(rel)
	if err != nil {
		return ingest.ArtifactSrc{}, err
	}
	*opened = append(*opened, f)
	sum, err := sha256File(s.o.Data, rel)
	if err != nil {
		return ingest.ArtifactSrc{}, err
	}
	return ingest.ArtifactSrc{Name: name, File: f, SHA256: sum}, nil
}

func sha256File(root *os.Root, rel string) (string, error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ingestAndCommit is steps 5 to 9: the M1 freeze, the safety verification, one transaction for the take and the finished row,
// Ears, and the work dir.
func (s *Service) ingestAndCommit(ctx context.Context, row tRow, st *staged, rep childReport, acct *cpuAcct) *runFailure {
	if err := s.o.Data.WriteFile(path.Join(st.rel, "request.json"), []byte(st.rec.requestJSON), 0o600); err != nil {
		return fail(ErrTransformFailed, "request.json could not be written")
	}
	tj, err := json.Marshal(s.transformDoc(ctx, row, st, rep, acct))
	if err != nil {
		return fail(ErrTransformFailed, "transform.json could not be built")
	}
	if err := s.o.Data.WriteFile(path.Join(st.rel, "transform.json"), tj, 0o600); err != nil {
		return fail(ErrTransformFailed, "transform.json could not be written")
	}
	var opened []fs.File
	defer func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}()
	srcSum, err := sha256File(s.o.Data, path.Join(st.rel, flacFile))
	if err != nil {
		return fail(ErrTransformFailed, "the encoded file could not be read")
	}
	src, err := s.o.Data.Open(path.Join(st.rel, flacFile))
	if err != nil {
		return fail(ErrTransformFailed, "the encoded file could not be read")
	}
	opened = append(opened, src)
	var arts []ingest.ArtifactSrc
	for _, name := range []string{"request.json", "transform.json"} {
		a, err := s.fileArtifact(path.Join(st.rel, name), name, &opened)
		if err != nil {
			return fail(ErrTransformFailed, "an artifact could not be read")
		}
		arts = append(arts, a)
	}
	slug := library.Slug(row.Title)
	if slug == "" {
		slug = "transform"
	}
	now := s.o.Now()
	res, err := s.o.Pipeline.Ingest(ctx, ingest.Request{TrackID: row.ID, Title: row.Title, Slug: slug, RecipeID: row.RecipeID,
		RequestID: st.rec.requestID, Seed: st.rec.seed, Source: src, SourceSHA256: srcSum, RecordedAt: now, Artifacts: arts})
	if err != nil {
		var ie *ingest.Error
		if errors.As(err, &ie) {
			return fail(ErrIngestFailedPrefix+ie.Code, "ingest failed: %s", ie.Detail)
		}
		return fail(ErrIngestFailedPrefix+ingest.CodeIO, "ingest failed")
	}
	// A journal of what the freeze claimed, so a crash before the commit can be cleaned by the next sweep.
	if jb, jerr := json.Marshal(res.Created); jerr == nil {
		_ = s.o.Data.WriteFile(path.Join(st.rel, "claimed.json"), jb, 0o600)
	}
	if f := s.fault(FaultAfterIngest); f != nil {
		return f // a crash: the freeze's files stay behind
	}
	discard := func() { _ = s.o.Pipeline.Discard(res) }
	if f := verifySafety(res, rep.Safety); f != nil {
		discard()
		return f
	}
	if existing, ok, qerr := s.existingIdentity(ctx, res); qerr != nil {
		discard()
		return fail(ErrTransformFailed, "the library could not be read")
	} else if ok {
		discard()
		return fail(ErrIngestFailedPrefix+"DUPLICATE_AUDIO", "identical audio already exists as take %s", existing)
	}
	take, err := s.newTake(ctx, row, st, res, now)
	if err != nil {
		discard()
		return fail(ErrTransformFailed, "the take could not be prepared")
	}
	cpu := acct.ms()
	var lost bool
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		lost = false
		r, err := tx.ExecContext(ctx, `UPDATE transforms SET state = 'done', pcm_sha256 = ?, cpu_ms = ?, progress_permille = 1000, finished_at = ?,
			owner = NULL, lease_expires_at = NULL, phase = NULL WHERE id = ? AND state = 'running' AND owner = ?`,
			rep.PCMSHA256, cpu, store.FormatTime(now), row.ID, s.owner())
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			lost = true
			return nil
		}
		if err := s.o.Library.InsertTake(ctx, tx, take); err != nil {
			return err
		}
		return auditTerminal(tx, AuditDone, now, auditDetails{ID: row.ID, Kind: string(row.Kind), CPUMS: cpu}, s.sinceStart(row, now))
	})
	if lost {
		discard()
		return &runFailure{code: ErrInterrupted, msg: "the lease was lost", lost: true}
	}
	if err != nil {
		discard()
		if existing, ok, qerr := s.existingIdentity(ctx, res); qerr == nil && ok {
			return fail(ErrIngestFailedPrefix+"DUPLICATE_AUDIO", "identical audio already exists as take %s", existing)
		}
		s.log().Warn("transform commit", "transform_id", row.ID, "err", s.clean(err.Error()))
		return fail(ErrIngestFailedPrefix+"DB_INSERT", "the take could not be recorded")
	}
	s.changed(row.ID)
	s.removeWork(row.ID)
	if s.o.Ears != nil {
		if _, err := s.o.Ears.AnalyzeTrack(context.WithoutCancel(ctx), row.ID, ears.AnalyzeOptions{}); err != nil {
			s.log().Warn("transform ears analysis", "transform_id", row.ID, "err", s.clean(err.Error()))
		}
	}
	return nil
}

// verifySafety is the bug alarm of D-T1-14.6: not a policy (the safety stage derives the gain and never fails on loudness) but
// a check that the pipeline's own measurement agrees with the report. true_peak <= -0.5 dBTP and the integrated loudness within
// 1.5 LU of target_lufs_effective, unless the limiter engaged for more than 10 % of the time, when it must still be within
// MaxLimitingLU below.
func verifySafety(res ingest.Result, sf safetyDoc) *runFailure {
	l := res.Loudness
	switch {
	case math.IsNaN(l.IntegratedLUFS) || math.IsNaN(l.TruePeakDBTP):
		return fail(ErrLoudnessUnsafe, "the loudness of the result could not be measured")
	case l.TruePeakDBTP > -0.5:
		return fail(ErrLoudnessUnsafe, "the true peak of the result is %.2f dBTP", l.TruePeakDBTP)
	case sf.Limiter.EngagedFraction > 0.10:
		if l.IntegratedLUFS < sf.TargetLUFSEffective-MaxLimitingLU {
			return fail(ErrLoudnessUnsafe, "the result is %.1f LU below its target", sf.TargetLUFSEffective-l.IntegratedLUFS)
		}
	case math.Abs(l.IntegratedLUFS-sf.TargetLUFSEffective) > 1.5:
		return fail(ErrLoudnessUnsafe, "the result misses its target loudness by %.1f LU", math.Abs(l.IntegratedLUFS-sf.TargetLUFSEffective))
	}
	return nil
}

// existingIdentity finds a take with the same M1 audio identity (frames hash and sample count).
func (s *Service) existingIdentity(ctx context.Context, res ingest.Result) (id string, ok bool, err error) {
	err = s.o.Store.R.QueryRowContext(ctx, `SELECT id FROM tracks WHERE frames_sha256 = ? AND total_samples = ?`,
		res.FramesSHA256, int64(res.Info.StreamInfo.TotalSamples)).Scan(&id) //nolint:gosec // FLAC totals are 36 bits
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return id, true, nil
}

// newTake builds the library write of the result (D-T1-1): parent, root and generation come from the primary input (a score
// render has none), the ledger operator is the kind.
func (s *Service) newTake(ctx context.Context, row tRow, st *staged, res ingest.Result, now time.Time) (library.NewTake, error) {
	si := res.Info.StreamInfo
	nt := library.NewTake{
		ID: row.ID, Title: row.Title, Slug: library.Slug(row.Title), RecipeID: row.RecipeID,
		DurationS: res.Info.Duration(), SampleRate: int(si.SampleRate), BitsPerSample: int(si.BitsPerSample), Channels: int(si.Channels),
		TotalSamples: int64(si.TotalSamples), //nolint:gosec // FLAC totals are 36 bits
		MasterPath:   res.MasterRelPath, MasterSHA256: res.MasterSHA256, MasterBytes: res.MasterBytes,
		SourceSHA256: res.MasterSHA256, FramesSHA256: res.FramesSHA256, AudioMD5: res.AudioMD5,
		ImportRule: "transform", PrincipalID: row.PrincipalID, RecordedAt: now,
		Loudness: res.Loudness, Spectrum: res.Spectrum, ToolVersion: res.ToolVersion, ReplayGainDB: res.ReplayGainDB,
		Origin: "transform", StatusSetBy: "system:transform", TransformID: row.ID,
	}
	if nt.Slug == "" {
		nt.Slug = "transform"
	}
	if row.Kind != KindScoreRender && len(st.inputs) > 0 {
		p := st.inputs[0]
		nt.ParentTrackID, nt.RootTrackID, nt.Generation = p.ref.TrackID, p.rootID, p.generation+1
	}
	for _, r := range []struct {
		kind string
		r    ingest.Rendition
	}{{"mp3_320", res.MP3}, {"peaks", res.Peaks}} {
		nt.Renditions = append(nt.Renditions, library.NewRendition{Kind: r.kind, SHA256: r.r.SHA256, Bytes: r.r.Bytes, Path: r.r.RelPath, Params: r.r.Params})
	}
	for _, a := range res.Artifacts {
		nt.Artifacts = append(nt.Artifacts, library.NewArtifact{Name: path.Base(a.RelPath), SHA256: a.SHA256, Path: a.RelPath, Bytes: a.Bytes})
	}
	rationale := ""
	if row.Rationale.Valid {
		rationale = row.Rationale.String
	}
	nt.Mutation = &library.NewMutation{Operator: string(row.Kind), Rationale: rationale,
		Params: map[string]any{"spec_hash": row.SpecHash, "kind": string(row.Kind)}}
	return nt, nil
}

// discardOrphan removes the leftovers of a crashed attempt: what the journal names (the master and the take files the freeze
// claimed) and the take directory. It mirrors ingest.Discard (there is no ingest.DiscardOrphan) and only ever touches
// audio/masters/<file> and takes/<id>/<file>.
func (s *Service) discardOrphan(id string) {
	rel, _ := s.work(id)
	if b, err := s.o.Data.ReadFile(path.Join(rel, "claimed.json")); err == nil {
		var created []string
		if json.Unmarshal(b, &created) == nil {
			s.discardPaths(created)
		}
	}
	s.discardStrayMasters(id)
	dir := path.Join("takes", id)
	if entries, err := fs.ReadDir(s.o.Data.FS(), dir); err == nil {
		var created []string
		for _, e := range entries {
			if !e.IsDir() {
				created = append(created, path.Join(dir, e.Name()))
			}
		}
		s.discardPaths(created)
	}
	_ = s.o.Data.Remove(dir)
}

// discardStrayMasters removes the master a crash left between the freeze (which claims audio/masters/<name>) and the journal
// write that would have named it. Such a file belongs to no tracks row, and the freeze tags every master with MM_TAKE_ID, so it
// is attributable without a journal: an unreferenced FLAC whose MM_TAKE_ID is this transform's id. Nothing else is touched.
func (s *Service) discardStrayMasters(id string) {
	const dir = "audio/masters"
	entries, err := fs.ReadDir(s.o.Data.FS(), dir)
	if err != nil {
		return
	}
	owned := map[string]bool{}
	rows, err := s.o.Store.R.Query(`SELECT master_path FROM tracks WHERE master_path LIKE 'audio/masters/%'`)
	if err != nil {
		return
	}
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			owned[p] = true
		}
	}
	if rows.Close() != nil || rows.Err() != nil {
		return
	}
	want := "MM_TAKE_ID=" + id
	for _, e := range entries {
		rel := path.Join(dir, e.Name())
		if e.IsDir() || owned[rel] || !strings.HasSuffix(e.Name(), ".flac") {
			continue
		}
		if s.masterTakeID(rel) == want {
			s.discardPaths([]string{rel})
		}
	}
}

// masterTakeID returns the MM_TAKE_ID=<id> comment of a master, or "" when it has none or cannot be read.
func (s *Service) masterTakeID(rel string) string {
	f, err := s.o.Data.Open(rel)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		return ""
	}
	for _, c := range info.Comments {
		if strings.HasPrefix(c, "MM_TAKE_ID=") {
			return c
		}
	}
	return ""
}

// discardPaths removes exactly the listed audio/masters/<file> and takes/<id>/<file> paths, then the then-empty take dirs.
func (s *Service) discardPaths(created []string) {
	dirs := map[string]bool{}
	for _, rel := range created {
		switch clean := path.Clean(rel); {
		case clean != rel:
			continue
		case strings.HasPrefix(rel, "audio/masters/") && strings.Count(rel, "/") == 2:
		case strings.HasPrefix(rel, "takes/") && strings.Count(rel, "/") == 2:
			dirs[path.Dir(rel)] = true
		default:
			continue
		}
		if err := s.o.Data.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log().Warn("transform: discard", "err", s.clean(err.Error()))
		}
	}
	for d := range dirs {
		_ = s.o.Data.Remove(d) // only succeeds when empty
	}
}
