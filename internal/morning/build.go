package morning

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Build failure codes, stored on the crate row by the service.
const (
	CodeLoudness    = "MORNING_CRATE_LOUDNESS"
	CodeSamples     = "MORNING_CRATE_SAMPLES"
	CodeInterrupted = "INTERRUPTED"
	CodeToolFailed  = "TOOL_FAILED"
)

// Build limits.
const (
	// LoudnessTolerance is the accepted distance (LU) of a chapter from TargetLUFS.
	LoudnessTolerance = 0.5
	fadeMS            = 30
	seekEverySeconds  = 2
	resampleSlack     = 2 // frames a resampled decode may differ from the plan
	decodeTimeout     = 5 * time.Minute
	flacTimeout       = 10 * time.Minute
	toolMaxStdout     = 1 << 20
	morningDir        = "derived/morning"
)

// BuildError is a failed side build. Code is one of the Code constants; Err never carries argv.
type BuildError struct {
	Code    string
	Chapter int // 1-based chapter number, 0 when not chapter specific
	Msg     string
	Err     error
}

func (e *BuildError) Error() string {
	s := e.Code + ": " + e.Msg
	if e.Chapter > 0 {
		s += " (chapter " + strconv.Itoa(e.Chapter) + ")"
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the cause.
func (e *BuildError) Unwrap() error { return e.Err }

// Source describes the master of one take. MasterPath is relative to the data directory.
type Source struct {
	TrackID    string
	MasterPath string
	SampleRate int
	Channels   int // 1 or 2
}

// SideInput is one side to build and the masters of its takes, keyed by TrackID.
type SideInput struct {
	Side    Side
	Sources map[string]Source
}

// ChapterLoudness is what was measured in the built file for one chapter.
type ChapterLoudness struct {
	N        int
	LUFS     float64
	TruePeak float64
}

// SideResult is a built side, claimed at RelPath (relative to the data directory).
type SideResult struct {
	SHA256    string
	RelPath   string
	Bytes     int64
	Samples   int64
	Chapters  []ChapterLoudness
	ByteRatio float64 // FLAC bytes over raw 24-bit PCM bytes
	Wall      time.Duration
}

// SideBuilder builds one side of a crate. Builder is the real one; morningtest has a fake.
type SideBuilder interface {
	BuildSide(ctx context.Context, in SideInput) (SideResult, error)
}

// Builder renders sides with ffmpeg and flac. Masters are only ever hard-linked into a work directory and read.
type Builder struct {
	data    *os.Root
	dataDir string
	tools   mediatool.Tools
	an      clip.WindowAnalyzer
}

var _ SideBuilder = (*Builder)(nil)

// NewBuilder returns a Builder over the data root (dataDir is its absolute path).
func NewBuilder(data *os.Root, dataDir string, tools mediatool.Tools, an clip.WindowAnalyzer) *Builder {
	return &Builder{data: data, dataDir: dataDir, tools: tools, an: an}
}

func toolFail(chapter int, msg string, err error) error {
	return &BuildError{Code: CodeToolFailed, Chapter: chapter, Msg: msg, Err: err}
}

// ctxOr maps a cancelled context to INTERRUPTED and anything else to TOOL_FAILED.
func ctxOr(ctx context.Context, chapter int, msg string, err error) error {
	if ctx.Err() != nil {
		return &BuildError{Code: CodeInterrupted, Chapter: chapter, Msg: "build cancelled", Err: ctx.Err()}
	}
	return toolFail(chapter, msg, err)
}

// decodeArgs decodes one master to the side format with the gain baked in: for mono masters only, an
// equal-power (-3.01 dB per side, so BS.1770 measures the mono loudness unchanged) pan, a resample only when the rate differs, the gain, then fades of fadeMS at both ends of a take of frames frames.
func decodeArgs(in, out string, src Source, gainCDB int, frames int64) []string {
	var af []string
	if src.Channels == 1 {
		af = append(af, "pan=stereo|c0=0.7071067811865476*c0|c1=0.7071067811865476*c0")
	}
	if src.SampleRate != SampleRate {
		af = append(af, "aresample="+strconv.Itoa(SampleRate)+":resampler=swr")
	}
	if gainCDB != 0 {
		af = append(af, fmt.Sprintf("volume=%.2fdB:precision=double", float64(gainCDB)/100))
	}
	fade := float64(fadeMS) / 1000
	af = append(af, fmt.Sprintf("afade=t=in:st=0:d=%.3f", fade),
		fmt.Sprintf("afade=t=out:st=%.3f:d=%.3f", float64(frames)/SampleRate-fade, fade))
	return []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-y", "-protocol_whitelist", "file,pipe",
		"-i", "file:" + in, "-map", "0:a:0", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact",
		"-af", strings.Join(af, ","),
		"-c:a", "pcm_s24le", "-ar", strconv.Itoa(SampleRate), "-ac", "2", "-f", "wav", "file:" + out}
}

// chapterTime formats a frame position as HH:MM:SS.mmm.
func chapterTime(frame int64) string {
	ms := frame * 1000 / SampleRate
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// flacArgs encodes the side WAV with seek points at every take start and every 2 s and the blind chapter tags. Each -T is one
// argument; there is no shell.
func flacArgs(in, out string, s Side) []string {
	args := []string{"--best", "--no-padding", "--silent", "--force", "-S", strconv.Itoa(seekEverySeconds) + "s"}
	for _, c := range s.Chapters {
		args = append(args, "-S", strconv.FormatInt(c.Start, 10))
	}
	for i, c := range s.Chapters {
		tag := fmt.Sprintf("CHAPTER%03d", i+1)
		args = append(args, "-T", tag+"="+chapterTime(c.Start), "-T", fmt.Sprintf("%sNAME=Take %d", tag, c.N))
	}
	return append(args, "-o", out, in)
}

// BuildSide renders in.Side to a 24-bit 48 kHz stereo FLAC under derived/morning/ and returns it.
func (b *Builder) BuildSide(ctx context.Context, in SideInput) (SideResult, error) {
	start := time.Now()
	s := in.Side
	if b.an == nil || len(s.Chapters) == 0 {
		return SideResult{}, toolFail(0, "side builder is not configured or the side is empty", nil)
	}
	for _, c := range s.Chapters {
		src, ok := in.Sources[c.TrackID]
		if !ok || (src.Channels != 1 && src.Channels != 2) || src.SampleRate <= 0 || src.MasterPath == "" {
			return SideResult{}, toolFail(c.N, "take has no usable master", nil)
		}
	}
	if err := b.data.MkdirAll("incoming", 0o755); err != nil {
		return SideResult{}, toolFail(0, "cannot create incoming", err)
	}
	rel := "incoming/morning-" + uuid.NewV7().String()
	if err := b.data.Mkdir(rel, 0o700); err != nil {
		return SideResult{}, toolFail(0, "cannot create work dir", err)
	}
	defer func() { _ = b.data.RemoveAll(rel) }()
	abs := b.dataDir + "/" + rel

	if err := b.writeSideWAV(ctx, rel, abs, in); err != nil {
		return SideResult{}, err
	}
	if _, err := mediatool.Run(ctx, b.tools.FLAC, flacArgs(abs+"/side.wav", abs+"/side.flac", s),
		mediatool.RunOpts{Timeout: flacTimeout, MaxStdout: toolMaxStdout}); err != nil {
		return SideResult{}, ctxOr(ctx, 0, "flac encode failed", err)
	}
	size, err := b.verifyFLAC(rel+"/side.flac", s)
	if err != nil {
		return SideResult{}, err
	}
	loud, err := b.checkLoudness(ctx, abs+"/side.flac", s)
	if err != nil {
		return SideResult{}, err
	}
	res := SideResult{Bytes: size, Samples: s.Samples, Chapters: loud,
		ByteRatio: float64(size) / (float64(s.Samples) * wavBytesFrame)}
	if res.SHA256, err = b.sha(rel + "/side.flac"); err != nil {
		return SideResult{}, toolFail(0, "cannot hash the side", err)
	}
	if err := b.data.Chmod(rel+"/side.flac", 0o444); err != nil {
		return SideResult{}, toolFail(0, "cannot seal the side", err)
	}
	res.RelPath = path.Join(morningDir, res.SHA256[:2], res.SHA256+".flac")
	if err := b.data.MkdirAll(path.Dir(res.RelPath), 0o755); err != nil {
		return SideResult{}, toolFail(0, "cannot create the morning directory", err)
	}
	if err := b.data.Link(rel+"/side.flac", res.RelPath); err != nil && !errors.Is(err, fs.ErrExist) {
		return SideResult{}, toolFail(0, "cannot claim the side", err)
	}
	res.Wall = time.Since(start)
	return res, nil
}

// writeSideWAV decodes every chapter and concatenates slate PCM and item frames into side.wav, sample exactly.
func (b *Builder) writeSideWAV(ctx context.Context, rel, abs string, in SideInput) error {
	s := in.Side
	out, err := b.data.OpenFile(rel+"/side.wav", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return toolFail(0, "cannot create the side wav", err)
	}
	defer func() { _ = out.Close() }()
	var parts []io.Reader
	var open []*os.File
	defer func() {
		for _, f := range open {
			_ = f.Close()
		}
	}()
	for _, c := range s.Chapters {
		src := in.Sources[c.TrackID]
		master := fmt.Sprintf("%s/m%03d%s", rel, c.N, path.Ext(src.MasterPath))
		if err := b.data.Link(src.MasterPath, master); err != nil { // read-only use: a link, never an output path
			return toolFail(c.N, "master unavailable", err)
		}
		want := c.End - c.AudioStart
		wavRel := fmt.Sprintf("%s/c%03d.wav", rel, c.N)
		if _, err := mediatool.Run(ctx, b.tools.FFmpeg, decodeArgs(b.dataDir+"/"+master, b.dataDir+"/"+wavRel, src, c.GainCDB, want),
			mediatool.RunOpts{Timeout: decodeTimeout, MaxStdout: toolMaxStdout}); err != nil {
			return ctxOr(ctx, c.N, "ffmpeg decode failed", err)
		}
		_ = b.data.Remove(master)
		f, err := b.data.Open(wavRel)
		if err != nil {
			return toolFail(c.N, "cannot open the decoded chapter", err)
		}
		open = append(open, f)
		st, err := f.Stat()
		if err != nil {
			return toolFail(c.N, "cannot stat the decoded chapter", err)
		}
		off, got, err := wavData(f, st.Size())
		if err != nil {
			return toolFail(c.N, "decoded chapter is not side-format PCM", err)
		}
		slack := int64(0)
		if src.SampleRate != SampleRate {
			slack = resampleSlack
		}
		if d := got - want; d > slack || d < -slack {
			return &BuildError{Code: CodeSamples, Chapter: c.N, Msg: fmt.Sprintf("decoded %d frames, planned %d", got, want)}
		}
		slate := SlatePCM(c.N)
		slateFrames := c.AudioStart - c.Start
		parts = append(parts, fitReader(slate, slateFrames*wavBytesFrame), fitReader2(io.NewSectionReader(f, off, got*wavBytesFrame), got*wavBytesFrame, want*wavBytesFrame))
	}
	if err := writeWAV(out, s.Samples, parts...); err != nil {
		return &BuildError{Code: CodeSamples, Msg: "side does not add up", Err: err}
	}
	return out.Close()
}

// fitReader yields exactly n bytes of b, truncated or zero padded.
func fitReader(b []byte, n int64) io.Reader {
	return fitReader2(bytes.NewReader(b), int64(len(b)), n)
}

// fitReader2 yields exactly n bytes of r (which holds have bytes): truncated or zero padded.
func fitReader2(r io.Reader, have, n int64) io.Reader {
	if have >= n {
		return io.LimitReader(r, n)
	}
	return io.MultiReader(r, &zeroReader{n: n - have})
}

// verifyFLAC checks STREAMINFO and the metadata layout of the encoded side and returns its size.
func (b *Builder) verifyFLAC(rel string, s Side) (int64, error) {
	f, err := b.data.Open(rel)
	if err != nil {
		return 0, toolFail(0, "cannot open the encoded side", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return 0, toolFail(0, "cannot stat the encoded side", err)
	}
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		return 0, toolFail(0, "encoded side is not FLAC", err)
	}
	si := info.StreamInfo
	if si.BitsPerSample != 24 || si.SampleRate != SampleRate || si.Channels != wavChannels {
		return 0, toolFail(0, fmt.Sprintf("encoded side is %d bit %d Hz %d ch", si.BitsPerSample, si.SampleRate, si.Channels), nil)
	}
	if got := int64(si.TotalSamples); got != s.Samples { // #nosec G115 -- 36-bit field
		return 0, &BuildError{Code: CodeSamples, Msg: fmt.Sprintf("encoded %d samples, planned %d", got, s.Samples)}
	}
	if info.Count(flacmeta.BlockVorbisComment) != 1 || info.Count(flacmeta.BlockCueSheet) != 0 {
		return 0, toolFail(0, "encoded side has an unexpected metadata layout", nil)
	}
	return st.Size(), nil
}

// checkLoudness measures every chapter in the built file: -16 LU within LoudnessTolerance, or, for a limited take, not above
// the target and its true peak not above the ceiling by more than the tolerance.
func (b *Builder) checkLoudness(ctx context.Context, absFLAC string, s Side) ([]ChapterLoudness, error) {
	out := make([]ChapterLoudness, 0, len(s.Chapters))
	for _, c := range s.Chapters {
		m, err := b.an.WindowLoudness(ctx, absFLAC, c.AudioStart, c.End)
		if err != nil {
			slog.Error("morning loudness check failed", "err", redact.Error(err))
			return nil, ctxOr(ctx, c.N, "loudness measurement failed", err)
		}
		bad := math.IsNaN(m.IntegratedLUFS) || math.IsNaN(m.TruePeakDBTP)
		if c.Limited {
			bad = bad || m.IntegratedLUFS > TargetLUFS+LoudnessTolerance || m.TruePeakDBTP > CeilingTP+LoudnessTolerance
		} else {
			bad = bad || math.Abs(m.IntegratedLUFS-TargetLUFS) > LoudnessTolerance
		}
		if bad {
			return nil, &BuildError{Code: CodeLoudness, Chapter: c.N,
				Msg: fmt.Sprintf("measured %.1f LUFS, %.1f dBTP; target %.1f", m.IntegratedLUFS, m.TruePeakDBTP, TargetLUFS)}
		}
		out = append(out, ChapterLoudness{N: c.N, LUFS: m.IntegratedLUFS, TruePeak: m.TruePeakDBTP})
	}
	return out, nil
}

func (b *Builder) sha(rel string) (string, error) {
	f, err := b.data.Open(rel)
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
