package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Lane E2-INT-B owns this file: the constructors of the ACE-Step backends (installed from init, because they live in the
// provider packages of lanes E2-PF and E2-PR) and the last wiring step.

func init() { e2ExtraBackends = acestepBackends }

// wireE2Hooks is the last E2 wiring step; Build calls it after every service exists. The wiring that needs the finished server
// (the media route) is bound by Build through lateHandler before this runs, so nothing is left to do here in E2a/E2b; E2c's
// trial and Suite hooks attach here.
func wireE2Hooks(s *Services) {}

// lateHandler is an http.Handler bound after construction: the media route is built last, but the fake worker's input fetcher
// and the media preflight need it. Until it is set it answers 503.
type lateHandler struct{ h atomic.Pointer[http.Handler] }

func (l *lateHandler) set(h http.Handler) { l.h.Store(&h) }

func (l *lateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p := l.h.Load(); p != nil && *p != nil {
		(*p).ServeHTTP(w, r)
		return
	}
	http.Error(w, "service unavailable", http.StatusServiceUnavailable)
}

// aceWeights are the revisions the ACE-Step endpoint's worker must report: the main bundle, plus the base DiT when
// MM_ACESTEP_TASKS names it (the same set `runpod provision` pins).
func aceWeights(cfg config.Config) map[string]string {
	tasks := acestep.NormalizeTasks(strings.Join(cfg.ACEStepTasks, ","))
	if tasks == "" {
		tasks = acestep.TasksTurbo
	}
	return acestep.WeightsFor(tasks)
}

// acestepBackends builds the ACE-Step backend of the set (called only under MM_ACESTEP=1): `fake-acestep` over MM_BACKEND=fake,
// `runpod-serverless-acestep` over runpod when RUNPOD_ACESTEP_ENDPOINT_ID is present. Without that id (or without runpod.env at
// all) no backend is built: the engine is registered but nothing renders it (reason keys_absent, logged once) and the M3
// backend runs alone.
func acestepBackends(in e2Inputs) ([]e2Extra, error) {
	m, cfg := in.m3, in.cfg
	weights := aceWeights(cfg)
	switch cfg.Backend {
	case config.BackendFake:
		fo := fake.Options{}
		if m.opts.FakeACE != nil {
			fo = *m.opts.FakeACE
		}
		fo.Clock = m.clk
		if fo.Worker == nil && m.Uploads != nil && (m.opts.Live || m.opts.FakeACEWorker != nil || m.opts.FakeACE != nil) {
			wo := fake.SimWorkerOptions{}
			if m.opts.FakeACEWorker != nil {
				wo = *m.opts.FakeACEWorker
			}
			io := fake.HandlerUploader{UploadHandler: m.Uploads.Handler(), StartHandler: m.Uploads.StartHandler(), FetchHandler: m.media}
			if wo.Uploader == nil {
				wo.Uploader = io
			}
			if wo.Fetcher == nil {
				wo.Fetcher = io // the digest check of every fetched input needs it (otherwise it is skipped)
			}
			if wo.Weights == nil {
				wo.Weights = weights // Weights == expect.weights, else the worker answers WEIGHTS_MISMATCH
			}
			if wo.Clock == nil {
				wo.Clock = m.clk
			}
			if wo.Tools == (mediatool.Tools{}) {
				wo.Tools = mediatool.Tools{FFmpeg: cfg.FFmpeg, FLAC: cfg.FLAC, Metaflac: cfg.Metaflac}
			}
			fo.Worker = fake.NewSimWorker(wo)
		}
		return []e2Extra{{Backend: fake.NewACEStep(fo), KeysLoaded: true, Expect: provider.WorkerExpect{Weights: weights}}}, nil
	case config.BackendRunPod:
		keys, ok, err := config.LoadRunPodKeys(cfg.SecretsDir)
		if err != nil {
			return nil, err
		}
		if ok {
			keys = runpod.KeysFor(keys, provider.EngineACEStep)
		}
		if !ok || keys.EndpointID == "" {
			slog.Info("acestep: no backend built", "reason", provider.ReasonKeysAbsent)
			return nil, nil
		}
		bo := runpod.BackendOptions{}
		if m.opts.RunPod != nil {
			bo = *m.opts.RunPod
		}
		bo.Keys, bo.Clock, bo.Engine = keys, m.clk, provider.EngineACEStep
		if bo.ProbeJSON == nil {
			bo.ProbeJSON = runpod.EmbeddedCapabilities
		}
		bo.MaxRuntime, bo.ColdStartS, bo.RecordDir = cfg.ACEStepMaxRuntime, cfg.ACEStepColdStartS, cfg.RunPodRecordDir
		if bo.Recorder == nil && cfg.RunPodRecordDir != "" {
			rec, err := runpod.NewFileRecorder(cfg.RunPodRecordDir, keys.EndpointID, keys.Jobs, keys.Read, keys.Write)
			if err != nil {
				return nil, err
			}
			bo.Recorder = rec
		}
		return []e2Extra{{Backend: runpod.NewBackend(bo), KeysLoaded: true, Expect: provider.WorkerExpect{Weights: weights}}}, nil
	}
	return nil, nil
}

// refClipResolver resolves a reference clip by the content digest a recipe pins (ref.clip_sha256). The clip cache is addressed by
// the clip's key (master, window, tools), not by its bytes, so the digest is mapped back through the recipe that pinned it: its
// reference track and window give the clip spec, the cache returns (or deterministically re-renders) the clip, and the result
// is served only when its digest is still the pinned one (C153.3, C165). A clip that no longer reproduces is ErrNotFound, which
// the input minter reports as a changed input.
type refClipResolver struct {
	st    *store.Store
	clips *clip.Service
}

func (r refClipResolver) Resolve(ctx context.Context, sha, ext string) (mediaserve.Object, error) {
	if ext != "flac" {
		return mediaserve.Object{}, mediaserve.ErrNotFound
	}
	var trackID string
	var fromDS, toDS int64
	err := r.st.R.QueryRowContext(ctx, `SELECT json_extract(request_json, '$.ref.track_id'), json_extract(request_json, '$.ref.from_ds'),
		json_extract(request_json, '$.ref.to_ds') FROM recipes WHERE engine = ? AND json_extract(request_json, '$.ref.clip_sha256') = ? LIMIT 1`,
		provider.EngineACEStep, sha).Scan(&trackID, &fromDS, &toDS)
	if errors.Is(err, sql.ErrNoRows) {
		return mediaserve.Object{}, mediaserve.ErrNotFound
	}
	if err != nil {
		return mediaserve.Object{}, fmt.Errorf("app: reference clip recipe: %w", err)
	}
	sp := clip.RefSpec{FromMS: fromDS * 100, ToMS: toDS * 100}
	err = r.st.R.QueryRowContext(ctx, `SELECT master_sha256, master_path, sample_rate, channels FROM tracks WHERE id = ? AND lossy = 0`, trackID).
		Scan(&sp.MasterSHA256, &sp.MasterPath, &sp.SampleRate, &sp.Channels)
	if errors.Is(err, sql.ErrNoRows) {
		return mediaserve.Object{}, mediaserve.ErrNotFound
	}
	if err != nil {
		return mediaserve.Object{}, fmt.Errorf("app: reference clip source: %w", err)
	}
	obj, err := r.clips.Reference(ctx, sp)
	if err != nil {
		return mediaserve.Object{}, fmt.Errorf("app: reference clip: %w", err)
	}
	if obj.SHA256 != sha {
		return mediaserve.Object{}, mediaserve.ErrNotFound
	}
	return mediaserve.Object{RelPath: obj.RelPath, ContentType: "audio/flac", ETag: sha}, nil
}
