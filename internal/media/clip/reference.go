package clip

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// E2 seam (C165, D-E2-9): the reference clip renderer. A window of 2..30 s of a frozen master is cut raw (unity gain, no
// loudness match, no fades) and tiled end to end to exactly 30.000 s, so ACE-Step's own preprocessing of reference audio
// never applies and the recipe can pin the clip's bytes. It lives in the same package and cache as duel clips under a
// different key prefix. The signatures are frozen by the contract freeze (E2-F2); lane E2-CL fills the bodies.

// RefDef is the renderer definition string hashed into every reference key.
const RefDef = "mm-refclip/1"

// RefMS is the length of every reference clip: exactly 30.000 s, 1 440 000 frames at 48 kHz.
const RefMS = 30000

// Reference window bounds in milliseconds and the grid of its ends.
const (
	RefMinWindowMS = 2000
	RefMaxWindowMS = 30000
	RefGridMS      = 100
)

// ErrNotImplemented marks a seam whose body has not landed.
var ErrNotImplemented = errors.New("clip: not implemented yet")

// Problem codes of Reference; the generation layer maps them to INVALID_ITEM reference_window and ENGINE_CAPABILITY
// src_format (C165).
const (
	CodeRefWindow      = "CLIP_REFERENCE_WINDOW"
	CodeRefSilent      = "CLIP_REFERENCE_SILENT"
	CodeUnsupportedSR  = "CLIP_UNSUPPORTED_RATE"
	refFrames          = RefMS * OutRate / 1000
	refMasterFlacProbe = ".flac"
)

// RefSpec is one reference clip: a window [FromMS, ToMS) of the master, 2..30 s on the 100 ms grid, inside the master.
type RefSpec struct {
	MasterSHA256, MasterPath string
	SampleRate, Channels     int
	FromMS, ToMS             int64
}

// RefObject is a rendered reference clip: the cache Object plus the content digest of its FLAC bytes, which is the
// clip_sha256 the recipe pins (C165 amended: the plan returned a bare Object, which has no content digest).
type RefObject struct {
	Object
	SHA256 string
}

func (sp RefSpec) samples() (start, end int64) {
	return sp.FromMS * int64(sp.SampleRate) / 1000, sp.ToMS * int64(sp.SampleRate) / 1000
}

func (s *Service) validateRef(sp RefSpec) error {
	switch {
	case sp.Channels != 1 && sp.Channels != 2:
		return problem.E(422, "CLIP_UNSUPPORTED_CHANNELS", fmt.Sprintf("clips need a mono or stereo master, this one has %d channels", sp.Channels),
			map[string]any{"channels": sp.Channels})
	case sp.SampleRate != 44100 && sp.SampleRate != 48000:
		return problem.E(422, CodeUnsupportedSR, fmt.Sprintf("reference clips need a 44.1 or 48 kHz master, this one is %d Hz", sp.SampleRate),
			map[string]any{"sample_rate": sp.SampleRate})
	case sp.MasterPath == "" || sp.MasterSHA256 == "":
		return internalErr("the reference request is incomplete")
	}
	n := sp.ToMS - sp.FromMS
	if sp.FromMS < 0 || n < RefMinWindowMS || n > RefMaxWindowMS || sp.FromMS%RefGridMS != 0 || sp.ToMS%RefGridMS != 0 {
		return problem.E(422, CodeRefWindow, "the reference window must be 2 to 30 s long on the 100 ms grid",
			map[string]any{"from_ms": sp.FromMS, "to_ms": sp.ToMS})
	}
	return nil
}

// refArgs is the render invocation for the work-dir paths: cut raw, resample to 48 kHz when needed, repeat the window
// end to end and stop at exactly refFrames. No gain, no fades.
func refArgs(workMaster, outPath string, sp RefSpec) []string {
	st, en := sp.samples()
	win := (sp.ToMS - sp.FromMS) * OutRate / 1000
	af := []string{fmt.Sprintf("atrim=start_sample=%d:end_sample=%d", st, en), "asetpts=PTS-STARTPTS"}
	if sp.SampleRate != OutRate {
		// swr may land a frame off the window length; pin it so the loop period is exact.
		af = append(af, "aresample="+strconv.Itoa(OutRate)+":resampler=swr",
			fmt.Sprintf("atrim=end_sample=%d", win), fmt.Sprintf("apad=whole_len=%d", win))
	}
	af = append(af, fmt.Sprintf("aloop=loop=-1:size=%d:start=0", win), fmt.Sprintf("atrim=end_sample=%d", refFrames), "asetpts=PTS-STARTPTS")
	return []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-i", "file:" + workMaster, "-map", "0:a:0", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact",
		"-af", strings.Join(af, ","),
		"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", "-compression_level", "5",
		"-f", "flac", "file:" + outPath}
}

func (s *Service) refObject(key string, sp RefSpec, size int64) Object {
	return Object{Key: key, RelPath: relPath(key), Bytes: size, DurationS: RefMS / 1000, Channels: sp.Channels}
}

// Reference renders (or finds) the reference clip. The returned RefObject's SHA256 is the clip_sha256 pinned in the recipe.
// A silent window is refused (CLIP_REFERENCE_SILENT) because ACE-Step drops a silent reference without an error.
func (s *Service) Reference(ctx context.Context, sp RefSpec) (RefObject, error) {
	if err := s.validateRef(sp); err != nil {
		return RefObject{}, err
	}
	key := RefKey(sp, s.an.ToolVersion())
	obj, err := s.refEnsure(ctx, key, sp)
	if err != nil {
		return RefObject{}, err
	}
	sum, err := s.digest(obj.RelPath)
	if err != nil {
		return RefObject{}, err
	}
	return RefObject{Object: obj, SHA256: sum}, nil
}

// refEnsure is Ensure for reference keys: cache, then the shared single-flight table, detached from the caller.
func (s *Service) refEnsure(ctx context.Context, key string, sp RefSpec) (Object, error) {
	if o, ok, err := s.refCached(key, sp); ok || err != nil {
		return o, err
	}
	s.mu.Lock()
	f := s.flights[key]
	if f == nil {
		f = &flight{done: make(chan struct{})}
		s.flights[key] = f
		go func(ctx context.Context) {
			ctx, cancel := context.WithTimeout(ctx, renderTimeout)
			defer cancel()
			f.obj, f.err = s.refRender(ctx, key, sp)
			s.mu.Lock()
			delete(s.flights, key)
			s.mu.Unlock()
			close(f.done)
		}(context.WithoutCancel(ctx))
	}
	s.mu.Unlock()
	select {
	case <-f.done:
		return f.obj, f.err
	case <-ctx.Done():
		return Object{}, ctx.Err()
	}
}

func (s *Service) refCached(key string, sp RefSpec) (Object, bool, error) {
	st, err := s.data.Lstat(relPath(key))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Object{}, false, nil
	case err != nil:
		return Object{}, false, fmt.Errorf("clip: stat: %w", err)
	case !st.Mode().IsRegular():
		return Object{}, false, internalErr("the clip cache entry is not a regular file")
	}
	return s.refObject(key, sp, st.Size()), true, nil
}

// publish makes the rendered work-dir file read-only and links it into the cache under key.
func (s *Service) publish(relClip, key string) error {
	if err := s.data.Chmod(relClip, 0o444); err != nil {
		return fmt.Errorf("clip: chmod: %w", err)
	}
	if err := s.data.MkdirAll(path.Dir(relPath(key)), 0o755); err != nil {
		return fmt.Errorf("clip: create cache dir: %w", err)
	}
	if err := s.data.Link(relClip, relPath(key)); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("clip: link into place: %w", err)
	}
	return nil
}

func (s *Service) refRender(ctx context.Context, key string, sp RefSpec) (Object, error) {
	if o, ok, err := s.refCached(key, sp); ok || err != nil {
		return o, err
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return Object{}, ctx.Err()
	}
	s.evict(ctx)

	rel, abs, master, err := s.workMaster(Spec{MasterPath: sp.MasterPath})
	if err != nil {
		return Object{}, err
	}
	defer func() { _ = s.data.RemoveAll(rel) }()
	if err := s.checkRefWindow(rel+"/"+master, sp); err != nil {
		return Object{}, err
	}
	st, en := sp.samples()
	m, err := s.an.WindowLoudness(ctx, abs+"/"+master, st, en)
	if err != nil {
		slog.Error("reference measure failed", "err", redact.Error(err))
		return Object{}, internalErr("the reference window could not be measured")
	}
	if m.IntegratedLUFS <= SilentLUFS {
		return Object{}, problem.E(422, CodeRefSilent, "the reference window is silent; pick a window with audio",
			map[string]any{"from_ms": sp.FromMS, "to_ms": sp.ToMS})
	}
	if _, err := mediatool.Run(ctx, s.tools.FFmpeg, refArgs(abs+"/"+master, abs+"/clip.flac", sp),
		mediatool.RunOpts{Timeout: renderTimeout, MaxStdout: maxRenderStdout}); err != nil {
		slog.Error("reference render failed", "err", redact.Error(err))
		return Object{}, internalErr("the reference clip could not be rendered")
	}
	size, err := s.verifyRef(rel+"/clip.flac", sp)
	if err != nil {
		slog.Error("reference_clip_invalid", "err", redact.Error(err))
		return Object{}, internalErr("the rendered reference clip is invalid")
	}
	if err := s.publish(rel+"/clip.flac", key); err != nil {
		return Object{}, err
	}
	return s.refObject(key, sp, size), nil
}

// checkRefWindow refuses a window that reaches past the end of a FLAC master (a lossy master is not probed here).
func (s *Service) checkRefWindow(relMaster string, sp RefSpec) error {
	if !strings.HasSuffix(relMaster, refMasterFlacProbe) {
		return nil
	}
	f, err := s.data.Open(relMaster)
	if err != nil {
		return fmt.Errorf("clip: open master: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("clip: stat master: %w", err)
	}
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		slog.Error("reference master unreadable", "err", redact.Error(err))
		return internalErr("the reference master could not be read")
	}
	_, end := sp.samples()
	if int64(info.StreamInfo.TotalSamples) < end { //nolint:gosec // bounded by the 36-bit field
		return problem.E(422, CodeRefWindow, "the reference window reaches past the end of the track",
			map[string]any{"from_ms": sp.FromMS, "to_ms": sp.ToMS})
	}
	return nil
}

// verifyRef checks STREAMINFO of the rendered file: exactly 30.000 s at 24 bit / 48 kHz with the source's channels.
func (s *Service) verifyRef(rel string, sp RefSpec) (int64, error) {
	f, err := s.data.Open(rel)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		return 0, err
	}
	si := info.StreamInfo
	if si.BitsPerSample != 24 || si.SampleRate != OutRate || int(si.Channels) != sp.Channels || int64(si.TotalSamples) != refFrames { //nolint:gosec // bounded by the 36-bit field
		return 0, fmt.Errorf("clip: stream info %d bit %d Hz %d ch %d samples, want 24 bit %d Hz %d ch %d samples",
			si.BitsPerSample, si.SampleRate, si.Channels, si.TotalSamples, OutRate, sp.Channels, refFrames)
	}
	return st.Size(), nil
}

// digest is the hex sha256 of a cached clip's bytes.
func (s *Service) digest(rel string) (string, error) {
	f, err := s.data.Open(rel)
	if err != nil {
		return "", fmt.Errorf("clip: open reference clip: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("clip: hash reference clip: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
