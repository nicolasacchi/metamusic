package clip

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"sync"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

const (
	renderTimeout   = 60 * time.Second
	renderSlots     = 2
	defaultCacheCap = 4 << 30
	clipsDir        = "derived/clips"
)

// flight is one in-progress render; every waiter on the key shares it.
type flight struct {
	done chan struct{}
	obj  Object
	err  error
}

// Service renders, caches and serves clips (C46).
type Service struct {
	data    *os.Root
	dataDir string
	tools   mediatool.Tools
	an      WindowAnalyzer
	lib     *library.Service
	signer  *serve.Signer
	opts    Options

	mu      sync.Mutex
	flights map[string]*flight
	sem     chan struct{}
}

// New builds the service.
func New(data *os.Root, dataDir string, tools mediatool.Tools, an WindowAnalyzer, lib *library.Service,
	signer *serve.Signer, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CacheBytes <= 0 {
		o.CacheBytes = defaultCacheCap
	}
	s := &Service{data: data, dataDir: dataDir, tools: tools, an: an, lib: lib, signer: signer, opts: o,
		flights: map[string]*flight{}, sem: make(chan struct{}, renderSlots)}
	if o.Render == nil {
		s.opts.Render = s.ffmpegRender
	}
	return s
}

// ToolVersion is the analyzer's version token ("ffmpeg <token>"); it is part of every clip key.
func (s *Service) ToolVersion() string { return s.an.ToolVersion() }

func internalErr(msg string) error { return problem.E(500, "INTERNAL", msg, nil) }

func (s *Service) validate(sp Spec) error {
	switch {
	case sp.Channels != 1 && sp.Channels != 2:
		return problem.E(422, "CLIP_UNSUPPORTED_CHANNELS", fmt.Sprintf("clips need a mono or stereo master, this one has %d channels", sp.Channels),
			map[string]any{"channels": sp.Channels})
	case sp.SampleRate <= 0 || sp.MasterPath == "" || sp.MasterSHA256 == "":
		return internalErr("the clip request is incomplete")
	case sp.GainCDB > 0 || sp.GainCDB < MaxCutCDB:
		return internalErr("the clip gain is out of range")
	case sp.W.StartMS < 0 || sp.W.EndMS <= sp.W.StartMS || sp.W.StartMS%GridMS != 0 || sp.W.EndMS%GridMS != 0:
		return internalErr("the clip window is invalid")
	}
	return nil
}

func relPath(key string) string { return clipsDir + "/" + key[:2] + "/" + key + ".flac" }

func objectOf(key string, sp Spec, size int64) Object {
	st, en := sp.Samples()
	return Object{Key: key, RelPath: relPath(key), Bytes: size,
		DurationS: float64(en-st) / float64(sp.SampleRate), Channels: sp.Channels}
}

// cached reports the rendered clip when its file exists.
func (s *Service) cached(key string, sp Spec) (Object, bool, error) {
	st, err := s.data.Lstat(relPath(key))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Object{}, false, nil
	case err != nil:
		return Object{}, false, fmt.Errorf("clip: stat: %w", err)
	case !st.Mode().IsRegular():
		return Object{}, false, internalErr("the clip cache entry is not a regular file")
	}
	return objectOf(key, sp, st.Size()), true, nil
}

// workMaster hard-links the master into a fresh work dir and returns the
// DataDir-relative and absolute work paths. The caller removes the dir.
func (s *Service) workMaster(sp Spec) (rel, abs string, err error) {
	if err := s.data.MkdirAll("incoming", 0o755); err != nil {
		return "", "", fmt.Errorf("clip: create incoming: %w", err)
	}
	rel = "incoming/clip-" + uuid.NewV7().String()
	if err := s.data.Mkdir(rel, 0o700); err != nil {
		return "", "", fmt.Errorf("clip: create work dir: %w", err)
	}
	if err := s.data.Link(sp.MasterPath, rel+"/master.flac"); err != nil {
		_ = s.data.RemoveAll(rel)
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", library.ErrMediaUnavailable
		}
		return "", "", fmt.Errorf("clip: link master: %w", err)
	}
	return rel, s.dataDir + "/" + rel, nil
}

// Measure measures the loudness of the window of sp.
func (s *Service) Measure(ctx context.Context, sp Spec) (Measure, error) {
	if err := s.validate(sp); err != nil {
		return Measure{}, err
	}
	rel, abs, err := s.workMaster(sp)
	if err != nil {
		return Measure{}, err
	}
	defer func() { _ = s.data.RemoveAll(rel) }()
	st, en := sp.Samples()
	m, err := s.an.WindowLoudness(ctx, abs+"/master.flac", st, en)
	if err != nil {
		slog.Error("clip measure failed", "err", redact.Error(err))
		return Measure{}, internalErr("the clip window could not be measured")
	}
	return m, nil
}

// Ensure renders the clip of sp into the cache if it is absent. The render is
// detached from the caller (D-M2b-1): a cancelled caller returns its context
// error while the render continues and fills the cache for the others.
func (s *Service) Ensure(ctx context.Context, sp Spec) (Object, error) {
	if err := s.validate(sp); err != nil {
		return Object{}, err
	}
	key := Key(sp, s.an.ToolVersion())
	if o, ok, err := s.cached(key, sp); ok || err != nil {
		return o, err
	}
	s.mu.Lock()
	f := s.flights[key]
	if f == nil {
		f = &flight{done: make(chan struct{})}
		s.flights[key] = f
		go s.run(context.WithoutCancel(ctx), f, key, sp)
	}
	s.mu.Unlock()
	select {
	case <-f.done:
		return f.obj, f.err
	case <-ctx.Done():
		return Object{}, ctx.Err()
	}
}

func (s *Service) run(ctx context.Context, f *flight, key string, sp Spec) {
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	f.obj, f.err = s.render(ctx, key, sp)
	s.mu.Lock()
	delete(s.flights, key)
	s.mu.Unlock()
	close(f.done)
}

func (s *Service) render(ctx context.Context, key string, sp Spec) (Object, error) {
	if o, ok, err := s.cached(key, sp); ok || err != nil {
		return o, err
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return Object{}, ctx.Err()
	}
	s.evict(ctx)

	rel, abs, err := s.workMaster(sp)
	if err != nil {
		return Object{}, err
	}
	defer func() { _ = s.data.RemoveAll(rel) }()
	if err := s.opts.Render(ctx, abs+"/master.flac", abs+"/clip.flac", sp); err != nil {
		slog.Error("clip render failed", "err", redact.Error(err))
		return Object{}, internalErr("the clip could not be rendered")
	}
	size, err := s.verify(rel+"/clip.flac", sp)
	if err != nil {
		slog.Error("clip_invalid", "err", redact.Error(err))
		return Object{}, internalErr("the rendered clip is invalid")
	}
	if err := s.data.Chmod(rel+"/clip.flac", 0o444); err != nil {
		return Object{}, fmt.Errorf("clip: chmod: %w", err)
	}
	if err := s.data.MkdirAll(path.Dir(relPath(key)), 0o755); err != nil {
		return Object{}, fmt.Errorf("clip: create cache dir: %w", err)
	}
	if err := s.data.Link(rel+"/clip.flac", relPath(key)); err != nil && !errors.Is(err, fs.ErrExist) {
		return Object{}, fmt.Errorf("clip: link into place: %w", err)
	}
	return objectOf(key, sp, size), nil
}

// verify checks STREAMINFO of the rendered file and returns its size.
func (s *Service) verify(rel string, sp Spec) (int64, error) {
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
	start, end := sp.Samples()
	want := (end - start) * OutRate / int64(sp.SampleRate)
	got := int64(si.TotalSamples) //nolint:gosec // bounded by the 36-bit field
	if si.BitsPerSample != 24 || si.SampleRate != OutRate || int(si.Channels) != sp.Channels || got < want-1 || got > want+1 {
		return 0, fmt.Errorf("clip: stream info %d bit %d Hz %d ch %d samples, want 24 bit %d Hz %d ch %d samples",
			si.BitsPerSample, si.SampleRate, si.Channels, got, OutRate, sp.Channels, want)
	}
	return st.Size(), nil
}
