// Package app is the one constructor of services, gate dependencies and
// api.Deps (C39). serve, dev, mcp-stdio and the test rig all build through
// Build, so the faces cannot drift.
package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/board"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/drafts"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/gate"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/oidc"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/songview"
	"github.com/nicolasacchi/metamusic/internal/songwriting"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/tape"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// Options tunes Build; every field is optional.
type Options struct {
	Now    func() time.Time   // nil => time.Now; servertest passes Rig.Clock.Now
	NewID  func() string      // nil => UUIDv7
	Limits ratelimit.Config   // nil => ratelimit.Default.Scaled(cfg.RateScale)
	Probe  *mediaserve.Object // what cmd's ensureProbe returns for serve/dev; nil => probe ops answer 503
	Notify notify.Notifier    // nil => ntfy (MM_NTFY_URL) or Nop, wrapped in notify.Async
	HTTP   *http.Client       // ntfy client; nil => 10 s timeout client
	// Version and Commit are reported by the system operations.
	Version, Commit string
	// M2b (C51.7). Media injects the clip and Tier A inputs (nil => probe cfg.FFmpeg).
	Media *MediaOptions
	// SlotCoin fixes the blind-duel slot coin (nil => crypto/rand); the parity rigs share one sequence.
	SlotCoin func() bool
	// DuelClips, when set, receives the renderer duels would use (nil when clips are unavailable)
	// and returns the one they use instead: a spy, or a fake in lanes without a clip service.
	DuelClips func(duels.ClipRenderer) duels.ClipRenderer
	// Rand is the source of confirmation tokens and runner instance ids; nil => crypto/rand. Rigs inject a
	// seeded deterministic reader so the parity rigs mint identical sequences (C86.1).
	Rand io.Reader
	// M3 (C39 amended): generation services, backend selection and test injection points.
	M3 M3Options
	// M4a (C114): test injection points of the OIDC client and the Subsonic failure sleep; nil in production.
	M4a M4aOptions
	// L1 (C87): the library-model services and the Reconciler's test seams.
	L1 L1Options
	// L2 (C118): the curation services and the tape pipeline's test seams.
	L2 L2Options
}

// Services is everything Build wires.
type Services struct {
	// Deps is the api.Deps every registered operation receives.
	Deps api.Deps
	// Server carries the shared services and gate dependencies for httpserver.New;
	// the caller adds SPA and MCPFactory.
	Server httpserver.Options

	Tokens  *auth.TokenStore
	Local   *auth.LocalAuth
	Devices *auth.DeviceStore
	Backups *backup.Service
	Exports *exports.Service
	Idem    *idem.Store
	Audit   *audit.Store
	Limiter *ratelimit.Limiter

	// M2b services. Clips is nil when ffmpeg lacks the clip capabilities.
	Clips *clip.Service
	Duels *duels.Service
	Ears  *ears.Service

	// M3 services. Backend is nil for MM_BACKEND=none and for runpod without runpod.env; Runner and
	// Reconciler are built (never started) when a backend exists and cfg.Runner is true: serve starts
	// them, tests drive them. Wake is the nil-safe nudge generation and upload call after a commit.
	Lineage    *lineage.Service
	Spend      *spend.Service
	Generation *generation.Service
	Jobs       *jobs.Service
	Provider   *jobs.ProviderAdmin
	Uploads    *upload.Service
	Backend    provider.Backend
	Fake       *fake.Backend // set when the backend is the in-process fake
	Gate       *provider.Gate
	Runner     *jobs.Runner
	Reconciler *jobs.Reconciler

	// M4a services (C114). OIDC is nil unless MM_OIDC_ISSUER is set; serve runs `go OIDC.Prefetch(ctx)` after Build.
	OIDC       *oidc.Verifier
	Connectors *connectors.Store
	Subsonic   *auth.SubsonicStore
	Covers     *cover.Service
	Downloads  *tagged.Service
	// L1 services (C87). Provenance is the Reconciler: serve runs its startup Settle and Run, mcp-stdio and the
	// rigs never start it (rigs call Rig.Settle).
	Muses      *muses.Service
	Songs      *songs.Service
	SongView   *songview.Service
	Sessions   *sessions.Service
	Picks      *picks.Service
	Sounds     *sounds.Service
	Board      *board.Service
	Provenance *provenance.Service
	// L2 services (C118). Syncer and the tape runner (Tapes.Run) are built, never started: serve starts them, tests drive
	// them through Rig.Settle and Rig.Tapes.RunOne.
	Records    *records.Service
	Syncer     *records.Syncer
	RecordView *recordview.Service
	Crates     *crates.Service
	Tapes      *tape.Service
	Curation   *curation.Service
	Albums     *albums.Service

	// ClipsMissing and TierAMissing are non-nil when ffmpeg lacks the capabilities (C51.7); the
	// clip-dependent operations then answer 503 and Tier A reports TOOL_MISSING.
	ClipsMissing, TierAMissing error
}

// Build constructs the services over st and data. Every store and service
// receives o.Now, which lets rig tests advance one clock.
func Build(ctx context.Context, cfg config.Config, sec config.Secrets, st *store.Store, data *os.Root, o Options) (*Services, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	newID := api.Deps{NewID: o.NewID}.NewIDFunc()
	limits := o.Limits
	if limits == nil {
		limits = ratelimit.Default.Scaled(max(cfg.RateScale, 1))
	}

	tokens := auth.NewTokenStore(st, sec.Key, now)
	local := auth.NewLocalAuth(st, sec.Key, cfg.PublicURL)
	local.UseClock(now)

	lib := library.New(st, data, now)
	signer := mediaserve.NewSigner(sec.Key, now)
	devices := auth.NewDeviceStore(st, sec.Key, now, newID)

	notifier := o.Notify
	if notifier == nil {
		if cfg.NtfyURL != "" {
			hc := o.HTTP
			if hc == nil {
				hc = &http.Client{Timeout: 10 * time.Second}
			}
			notifier = notify.NewNtfy(cfg.NtfyURL, sec.NtfyToken, hc)
		} else {
			notifier = notify.Nop{}
		}
		notifier = notify.Async(notifier, 10*time.Second)
	}

	judg := judgments.New(st, lib, now, newID)
	listen := listening.New(st, now)
	pls := playlists.New(st, lib, now, newID)
	drf := drafts.New(st, lib, now, newID)
	sw := songwriting.New(st)
	drf.UseSongwriting(sw)
	exp := exports.New(exports.Options{Store: st, Data: data, Lib: lib, Playlists: pls, Ratings: judg, Signer: signer, Now: now, NewID: newID})
	bak := backup.New(backup.Options{Store: st, DataDir: cfg.DataDir, BackupDir: cfg.BackupDir, Exports: exports.NewDBSource(st, lib, pls),
		Now: now, NewID: newID, Version: o.Version, Notify: notifier})
	ctxb := trackctx.New(lib, judg, listen, signer, cfg.PublicURL, now)
	ctxb.UseSongwriting(sw)

	// M2b: the clip cache pins the clips of open duels, which only the duels service knows;
	// the closure resolves it after construction (clip must not import duels, C43).
	var ds *duels.Service
	media, err := NewMedia(ctx, cfg, st, data, lib, signer, now, func(ctx context.Context) (map[string]struct{}, error) {
		if ds == nil {
			return nil, nil
		}
		return ds.OpenClipKeys(ctx)
	}, o.Media)
	if err != nil {
		return nil, err
	}
	var renderer duels.ClipRenderer // never a typed-nil *clip.Service
	if media.Clips != nil {
		renderer = media.Clips
	}
	if o.DuelClips != nil {
		renderer = o.DuelClips(renderer)
	}
	ds = duels.New(duels.Options{Store: st, Library: lib, Clips: renderer, Signer: signer, Now: now, NewID: newID, Rand: o.SlotCoin})
	media.Ears.UseJudgments(ds)
	ctxb.UseM2b(media.Ears, ds) // perception from Ears, ladders from duels (C51.8)
	resolvers := []mediaserve.Resolver{lib, exp}
	if media.Clips != nil {
		resolvers = append(resolvers, media.Clips)
	}
	covers := cover.New(data, lib, now)
	downloads := tagged.New(data, lib, covers, now)
	resolvers = append(resolvers, m4aResolvers(covers, downloads)...)
	aud := audit.NewStore(st, newID)
	limiter := ratelimit.New(limits, now)

	wake := &wakeRef{}
	var pipe *ingest.Pipeline // the generation ingest bridge's master pipeline (nil without ffmpeg)
	if media.Analyzer != nil {
		abs, err := filepath.Abs(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		pipe = ingest.New(data, abs, mediatool.Tools{FFmpeg: cfg.FFmpeg, FLAC: cfg.FLAC, Metaflac: cfg.Metaflac}, media.Analyzer, now)
	}
	m3, err := buildM3(ctx, cfg, sec, st, data, lib, notifier, now, newID, o.Rand, o.M3, wake, pipe, media.Ears)
	if err != nil {
		return nil, err
	}

	m4a := buildM4a(m4aInputs{cfg: cfg, sec: sec, st: st, data: data, lib: lib, judg: judg, pls: pls, listen: listen, audit: aud,
		limiter: limiter, notifier: notifier, now: now, newID: newID, rnd: o.Rand, version: o.Version, covers: covers, downloads: downloads, ctxb: ctxb}, o.M4a)

	l1 := buildL1(st, now, newID, ds, media.Ears, o.L1)
	ctxb.UseLibrary(libraryReader{l1.SongView})
	lib.UseMuseCounter(func(ctx context.Context) (int, error) {
		p, err := l1.Muses.List(ctx, muses.ListFilter{Limit: 1})
		return p.Total, err
	})

	l2 := buildL2(l2Inputs{cfg: cfg, st: st, data: data, lib: lib, signer: signer, covers: covers, muses: l1.Muses, songs: l1.Songs, ears: media.Ears,
		version: o.Version, now: now, newID: newID}, o.L2)
	resolvers = append(resolvers, l2.Resolvers...)
	wireL2Curation(st, l1.SongView, ctxb)

	s := &Services{
		Records: l2.Records, Syncer: l2.Syncer, RecordView: l2.RecordView, Crates: l2.Crates, Tapes: l2.Tapes, Curation: l2.Curation, Albums: l2.Albums,
		OIDC: m4a.OIDC, Connectors: m4a.Connectors, Subsonic: m4a.Subsonic, Covers: covers, Downloads: downloads,
		Muses: l1.Muses, Songs: l1.Songs, SongView: l1.SongView, Sessions: l1.Sessions, Picks: l1.Picks, Sounds: l1.Sounds,
		Board: l1.Board, Provenance: l1.Provenance,
		Lineage: m3.Lineage, Spend: m3.Spend, Generation: m3.Generation, Jobs: m3.Jobs, Provider: m3.Provider, Uploads: m3.Uploads,
		Backend: m3.Backend, Fake: m3.Fake, Gate: m3.Gate, Runner: m3.Runner, Reconciler: m3.Reconciler,
		Tokens:  tokens,
		Local:   local,
		Devices: devices,
		Backups: bak,
		Exports: exp,
		Idem:    idem.NewStore(st, now),
		Audit:   aud,
		Limiter: limiter,
		Clips:   media.Clips,
		Duels:   ds,
		Ears:    media.Ears,

		ClipsMissing: media.ClipsMissing,
		TierAMissing: media.TierAMissing,
	}
	s.Deps = api.Deps{
		Store: st, Tokens: tokens, Cfg: cfg, Version: o.Version, Commit: o.Commit, Started: now(),
		Library: lib, Signer: signer, Probe: o.Probe, Now: now, NewID: o.NewID,
		Judgments: judg, Drafts: drf, Playlists: pls, Exports: exp, Backups: bak, Context: ctxb, Listening: listen,
		Audit: s.Audit, Devices: devices, Notify: notifier,
		Duels: ds, Ears: media.Ears, Clips: media.Clips,
		Muses: l1.Muses, Songs: l1.Songs, SongView: l1.SongView, Sessions: l1.Sessions, Picks: l1.Picks, Sounds: l1.Sounds, Board: l1.Board,
		Generation: m3.Generation, Spend: m3.Spend, Jobs: m3.Jobs, Provider: m3.Provider, Lineage: m3.Lineage, Songwriting: sw,
		Connectors: m4a.Connectors, OIDC: m4a.OIDC, Subsonic: m4a.Subsonic, Covers: covers, Downloads: downloads, Prompts: m4a.Prompts,
		Records: l2.Records, RecordView: l2.RecordView, Crates: l2.Crates, Tapes: l2.Tapes, Curation: l2.Curation, Albums: l2.Albums,
	}
	s.Server = httpserver.Options{
		Cfg: cfg, Sec: sec, Store: st, Tokens: tokens, Sessions: local,
		Version: o.Version, Commit: o.Commit, Started: s.Deps.Started,
		Library: lib, Signer: signer, Probe: o.Probe, Media: mediaserve.Handler(signer, data, mediaserve.Chain(resolvers...), o.Probe), Now: now,
		Audit: s.Audit, Idem: s.Idem, Limiter: s.Limiter, NewID: o.NewID,
		Judgments: judg, Drafts: drf, Playlists: pls, Exports: exp, Backups: bak, Context: ctxb, Listening: listen,
		Devices: devices, DeviceStore: devices, Notify: notifier,
		Duels: ds, Ears: media.Ears, Clips: media.Clips,
		Generation: m3.Generation, Spend: m3.Spend, Jobs: m3.Jobs, Provider: m3.Provider, Lineage: m3.Lineage, Uploads: m3.Uploads, Songwriting: sw,
		Muses: l1.Muses, Songs: l1.Songs, SongView: l1.SongView, LibSessions: l1.Sessions, Picks: l1.Picks, Sounds: l1.Sounds, Board: l1.Board,
		Records: l2.Records, RecordView: l2.RecordView, Crates: l2.Crates, Tapes: l2.Tapes, Curation: l2.Curation, Albums: l2.Albums,
		BaseContext: o.M3.Shutdown,
		Connectors:  m4a.ConnVerifier, PRM: m4a.PRM, Subsonic: m4a.SubsonicHandler, ConnectorStore: m4a.Connectors, OIDC: m4a.OIDC,
		SubsonicStore: m4a.Subsonic, Covers: covers, Downloads: downloads, Prompts: m4a.Prompts,
	}
	return s, nil
}

// Gated builds the Huma API and the gate around it without an HTTP listener,
// from the same dependencies and gate stores as Server. mcp-stdio uses it.
func (s *Services) Gated() (huma.API, http.Handler) {
	mux := http.NewServeMux()
	a := api.New(mux, s.Deps)
	return a, gate.New(gate.Options{API: a, Inner: mux, Audit: s.Audit, Idem: s.Idem, Limiter: s.Limiter, Now: s.Deps.Now})
}

// Close releases background work started by Build: it refuses new backups and
// waits for running ones until ctx ends.
func (s *Services) Close(ctx context.Context) error { return s.Backups.Close(ctx) }
