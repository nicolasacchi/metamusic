// Package transformtest is TEST-ONLY: fakes of the transform runner's collaborators, so the runner, queue, quota and cancel
// tests run instantly and deterministically without ffmpeg, flac or the render child. Import it only from _test.go files and
// servertest; it is listed among the helper packages TestNoTestHelpersInBinary keeps out of the release binary. The production
// wiring (internal/app/t1.go) builds the exec supervisor and the tool-backed Media and never selects a fake: tests inject one
// through the service Options.
package transformtest

import (
	"context"
	"crypto/sha256"
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
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// ---- FakeChild ----

// FakeChild is a transform.ChildRunner that renders no DSP. It "runs" for Duration (on the time package's clock, so a
// testing/synctest bubble advances it), reports Progress values evenly across that time, honours ctx, and writes a valid
// out.pcm24 plus report.json (a 24-bit stereo sine with the length of the first input) unless ExitCode is non-zero.
type FakeChild struct {
	Duration time.Duration
	ExitCode int
	// Progress are the permille values reported, evenly spread over Duration.
	Progress []int
	// CPU is the CPU time reported; zero means Duration.
	CPU time.Duration
	// Err is returned as the run's error (for example transform.ErrChildDeadline).
	Err error
	// Hang makes the run wait for ctx and nothing else.
	Hang bool
	// StderrTail is reported with a non-zero exit.
	StderrTail string
	// Same gives every spec the same audio; by default the audio varies with the job's seed and parameters, so two
	// different specs never collide on M1's audio identity.
	Same bool
	// Amp is the peak amplitude of the default sine (default 0.1); 0.13 reads close to -16 LUFS on a real measurement of the 400 Hz tone.
	Amp float64
	// WriteOut replaces the default output; it receives the absolute work dir and the parsed job.
	WriteOut func(dir string, job FakeJob) error

	mu   sync.Mutex
	runs int
	dirs []string
}

// FakeJob is the part of job.json a fake needs.
type FakeJob struct {
	Kind       string          `json:"kind"`
	Rate       int             `json:"rate"`
	Seed       int64           `json:"seed"`
	Params     json.RawMessage `json:"params"`
	TargetLUFS float64         `json:"target_lufs"`
	Inputs     []struct {
		Role   string `json:"role"`
		File   string `json:"file"`
		Frames int64  `json:"frames"`
	} `json:"inputs"`
}

// Runs is how many times the child ran.
func (f *FakeChild) Runs() int { f.mu.Lock(); defer f.mu.Unlock(); return f.runs }

// Dirs are the work dirs the child ran in, in order.
func (f *FakeChild) Dirs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dirs...)
}

// Run implements transform.ChildRunner.
func (f *FakeChild) Run(ctx context.Context, j transform.ChildJob, progress transform.ChildProgress) (transform.ChildResult, error) {
	f.mu.Lock()
	f.runs++
	f.dirs = append(f.dirs, j.Dir)
	f.mu.Unlock()
	cpu := f.CPU
	if cpu == 0 {
		cpu = f.Duration
	}
	res := transform.ChildResult{CPU: cpu}
	if f.Hang {
		<-ctx.Done()
		return res, ctx.Err()
	}
	steps := max(len(f.Progress), 1)
	for i := 0; i < steps; i++ {
		if d := f.Duration / time.Duration(steps); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return res, ctx.Err()
			case <-t.C:
			}
		}
		if i < len(f.Progress) && progress != nil {
			progress(f.Progress[i], "rendering")
		}
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if f.Err != nil {
		return res, f.Err
	}
	if f.ExitCode != 0 {
		res.ExitCode, res.StderrTail = f.ExitCode, f.StderrTail
		return res, nil
	}
	raw, err := os.ReadFile(filepath.Join(j.Dir, "job.json"))
	if err != nil {
		return res, fmt.Errorf("fake child: %w", err)
	}
	var job FakeJob
	if err := json.Unmarshal(raw, &job); err != nil || len(job.Inputs) == 0 {
		return res, errors.New("fake child: unreadable job.json")
	}
	write := f.WriteOut
	if write == nil {
		write = f.WriteDefault
	}
	if err := write(j.Dir, job); err != nil {
		return res, err
	}
	return res, nil
}

// WriteDefault writes the default output of a successful run into dir: out.pcm24 (a 24-bit stereo sine as long as the first
// input) and a matching report.json. A WriteOut hook may call it and then alter the result.
func (f *FakeChild) WriteDefault(dir string, job FakeJob) error {
	frames := job.Inputs[0].Frames
	cycles := 40.0 // whole cycles per block, so the block tiles without a seam
	if !f.Same {
		h := sha256.Sum256(append([]byte(fmt.Sprintf("%s|%d|", job.Kind, job.Seed)), job.Params...))
		cycles = float64(10 + binary.BigEndian.Uint16(h[:2])%190)
	}
	amp := f.Amp
	if amp == 0 {
		amp = 0.1
	}
	const block = 4800
	tile := make([]byte, 6*block)
	for i := 0; i < block; i++ {
		v := int32(math.Round(amp * math.Sin(2*math.Pi*cycles*float64(i)/block) * 8388607))
		for c := 0; c < 2; c++ {
			o := i*6 + c*3
			tile[o], tile[o+1], tile[o+2] = byte(v), byte(v>>8), byte(v>>16) //nolint:gosec // G115: the low 24 bits are the sample
		}
	}
	buf := make([]byte, 6*frames)
	for o := 0; o < len(buf); o += len(tile) {
		copy(buf[o:], tile)
	}
	if err := os.WriteFile(filepath.Join(dir, "out.pcm24"), buf, 0o600); err != nil {
		return err
	}
	sum := sha256.Sum256(buf)
	rep := map[string]any{
		"pcm_sha256": hex.EncodeToString(sum[:]), "frames": frames, "units": 1000,
		"safety": map[string]any{"gain_db": 0.0, "limiter": map[string]any{"max_reduction_db": 0.0, "engaged_fraction": 0.0},
			"target_lufs_requested": job.TargetLUFS, "target_lufs_effective": job.TargetLUFS, "lufs_in": job.TargetLUFS,
			"lufs_out": job.TargetLUFS, "true_peak_dbtp": -3.0},
		"warnings": []any{}, "beat_phase": []any{}, "voices": nil, "blind_spots": []string{},
	}
	b, _ := json.Marshal(rep)
	return os.WriteFile(filepath.Join(dir, "report.json"), b, 0o600)
}

// ---- FakeMedia ----

// FakeSrc is what a fake decode produces for one master: its frame count and channel count.
type FakeSrc struct {
	Frames   int64
	Channels int
}

// FakeMedia is a transform.Media without tools. Decode writes Frames frames of a ramp at the channel count of the source
// (looked up by the master's file name, else Default); Encode copies the PCM to the output file (not a real FLAC: pair it with
// FakeIngest, or use transform.ToolMedia for the media tier).
type FakeMedia struct {
	Sources map[string]FakeSrc
	Default FakeSrc
	// DecodeErr and EncodeErr make the step fail.
	DecodeErr, EncodeErr error
	// DecodeTime and EncodeTime are the tool wall times reported (they count toward cpu_ms).
	DecodeTime, EncodeTime time.Duration

	mu       sync.Mutex
	decoded  int
	encoded  int
	lastRate int
}

// Decoded and Encoded count the calls.
func (m *FakeMedia) Decoded() int { m.mu.Lock(); defer m.mu.Unlock(); return m.decoded }

// Encoded counts Encode calls; LastRate is the rate of the latest one.
func (m *FakeMedia) Encoded() int { m.mu.Lock(); defer m.mu.Unlock(); return m.encoded }

// LastRate is the sample rate of the latest Encode call.
func (m *FakeMedia) LastRate() int { m.mu.Lock(); defer m.mu.Unlock(); return m.lastRate }

// Decode implements transform.Media.
func (m *FakeMedia) Decode(ctx context.Context, src, dst string) (time.Duration, error) {
	m.mu.Lock()
	m.decoded++
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if m.DecodeErr != nil {
		return m.DecodeTime, m.DecodeErr
	}
	s, ok := m.Sources[filepath.Base(src)]
	if !ok {
		s = m.Default
	}
	if s.Frames == 0 {
		s = FakeSrc{Frames: 4800, Channels: 2}
	}
	buf := make([]byte, 4*int(s.Frames)*s.Channels)
	tile := make([]byte, 4*1000*s.Channels)
	for i := 0; i < 1000; i++ {
		for c := 0; c < s.Channels; c++ {
			binary.LittleEndian.PutUint32(tile[(i*s.Channels+c)*4:], math.Float32bits(float32(i)/2000))
		}
	}
	for o := 0; o < len(buf); o += len(tile) {
		copy(buf[o:], tile)
	}
	return m.DecodeTime, os.WriteFile(dst, buf, 0o600)
}

// Encode implements transform.Media.
func (m *FakeMedia) Encode(ctx context.Context, pcm, out string, rate int) (time.Duration, error) {
	m.mu.Lock()
	m.encoded++
	m.lastRate = rate
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if m.EncodeErr != nil {
		return m.EncodeTime, m.EncodeErr
	}
	b, err := os.ReadFile(pcm) //nolint:gosec // G304: a test path chosen by the caller
	if err != nil {
		return 0, err
	}
	return m.EncodeTime, os.WriteFile(out, b, 0o600) //nolint:gosec // G703: a test path chosen by the caller
}

// Versions implements transform.Media.
func (m *FakeMedia) Versions(context.Context) transform.ToolVersions {
	return transform.ToolVersions{FFmpeg: "fake-ffmpeg 1", FLAC: "fake-flac 1"}
}

// ---- FakeIngest ----

// FakeIngest is a transform.Ingester that freezes nothing for real: it stores the source as the master (mode 0444) and the
// artifacts under takes/<id>/, hashes the bytes as the audio identity and reports a 24-bit stereo FLAC of the right length. It
// honours Discard exactly like ingest.Pipeline (only what Result.Created lists).
type FakeIngest struct {
	Data *os.Root
	Rate int
	// Loudness is the measurement reported; zero means -16 LUFS at -3 dBTP.
	Loudness analysis.Loudness
	// Err makes Ingest fail with it.
	Err error

	mu        sync.Mutex
	ingests   int
	discarded int
}

// Ingests and Discards count the calls.
func (f *FakeIngest) Ingests() int { f.mu.Lock(); defer f.mu.Unlock(); return f.ingests }

// Discards counts Discard calls.
func (f *FakeIngest) Discards() int { f.mu.Lock(); defer f.mu.Unlock(); return f.discarded }

// Ingest implements transform.Ingester.
func (f *FakeIngest) Ingest(ctx context.Context, req ingest.Request) (ingest.Result, error) {
	f.mu.Lock()
	f.ingests++
	f.mu.Unlock()
	if f.Err != nil {
		return ingest.Result{}, f.Err
	}
	if err := ctx.Err(); err != nil {
		return ingest.Result{}, &ingest.Error{Code: ingest.CodeToolFailed, Detail: "cancelled", Err: err}
	}
	src, ok := req.Source.(interface{ Read([]byte) (int, error) })
	if !ok {
		return ingest.Result{}, &ingest.Error{Code: ingest.CodeIO, Detail: "no source"}
	}
	var b []byte
	buf := make([]byte, 1<<16)
	for {
		n, err := src.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	sum := sha256.Sum256(b)
	sumHex := hex.EncodeToString(sum[:])
	rate := f.Rate
	if rate == 0 {
		rate = 48000
	}
	frames := uint64(len(b) / 6)
	master := fmt.Sprintf("audio/masters/%s__%d__fake-%s.flac", req.Slug, req.Seed, sumHex[:12])
	var created []string
	if err := f.Data.MkdirAll("audio/masters", 0o755); err != nil {
		return ingest.Result{}, err
	}
	if err := f.Data.WriteFile(master, b, 0o444); err != nil {
		return ingest.Result{}, err
	}
	created = append(created, master)
	if err := f.Data.MkdirAll(path.Join("takes", req.TrackID), 0o755); err != nil {
		return ingest.Result{}, err
	}
	var arts []ingest.Rendition
	for _, a := range req.Artifacts {
		ab, err := readAll(a.File)
		if err != nil {
			return ingest.Result{}, err
		}
		rel := path.Join("takes", req.TrackID, a.Name)
		if err := f.Data.WriteFile(rel, ab, 0o444); err != nil {
			return ingest.Result{}, err
		}
		created = append(created, rel)
		arts = append(arts, ingest.Rendition{Kind: "artifact", RelPath: rel, SHA256: a.SHA256, Bytes: int64(len(ab))})
	}
	l := f.Loudness
	if l.IntegratedLUFS == 0 && l.TruePeakDBTP == 0 {
		l = analysis.Loudness{IntegratedLUFS: -16, TruePeakDBTP: -3}
	}
	mp3 := hex.EncodeToString(sha256.New().Sum([]byte("mp3" + sumHex)))[:64]
	peaks := hex.EncodeToString(sha256.New().Sum([]byte("peaks" + sumHex)))[:64]
	return ingest.Result{
		MasterRelPath: master, MasterSHA256: sumHex, FramesSHA256: sumHex, AudioMD5: strings.Repeat("0", 32), MasterBytes: int64(len(b)),
		Info:     flacmeta.Info{StreamInfo: flacmeta.StreamInfo{SampleRate: uint32(rate), Channels: 2, BitsPerSample: 24, TotalSamples: frames}}, //nolint:gosec // synthetic
		MP3:      ingest.Rendition{Kind: "mp3", RelPath: "derived/" + mp3[:2] + "/" + mp3 + ".mp3", SHA256: mp3, Bytes: 1},
		Peaks:    ingest.Rendition{Kind: "peaks", RelPath: "derived/" + peaks[:2] + "/" + peaks + ".peaks.json", SHA256: peaks, Bytes: 1},
		Loudness: l, ToolVersion: "fake", Artifacts: arts, Created: created,
	}, nil
}

func readAll(f fs.File) ([]byte, error) {
	var b []byte
	buf := make([]byte, 1<<16)
	for {
		n, err := f.Read(buf)
		b = append(b, buf[:n]...)
		if errors.Is(err, io.EOF) {
			return b, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// Discard implements transform.Ingester: it removes exactly Created, as ingest.Pipeline.Discard does.
func (f *FakeIngest) Discard(r ingest.Result) error {
	f.mu.Lock()
	f.discarded++
	f.mu.Unlock()
	dirs := map[string]bool{}
	for _, rel := range r.Created {
		if strings.HasPrefix(rel, "takes/") {
			dirs[path.Dir(rel)] = true
		}
		_ = f.Data.Remove(rel)
	}
	for d := range dirs {
		_ = f.Data.Remove(d)
	}
	return nil
}
