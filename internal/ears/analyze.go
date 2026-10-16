package ears

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"uuid"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/npy"
	"github.com/nicolasacchi/metamusic/internal/analysis/spectro"
	"github.com/nicolasacchi/metamusic/internal/analysis/tokens"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// maxSemanticBytes caps the semantic.npy the token-loop analyzer reads (C49.3).
const maxSemanticBytes = 8 << 20

// plan says which analyzers a track needs.
type plan struct {
	silence, clipping, tokens, spectro, tempo bool
	hasSemantic                               bool
	art                                       library.ArtifactRef
	secs                                      library.Sections
	overlay                                   spectro.Overlay
}

func (p plan) any() bool { return p.silence || p.clipping || p.tokens || p.spectro || p.tempo }

// needed lists the analyzers to run, in result order.
func (p plan) needed() []string {
	var out []string
	for _, n := range []struct {
		name string
		on   bool
	}{{anSilence, p.silence}, {anClipping, p.clipping}, {anTokenLoops, p.tokens}, {anSpectrogram, p.spectro}, {anTempo, p.tempo}} {
		if n.on {
			out = append(out, n.name)
		}
	}
	return out
}

// applicable lists the analyzers that apply to the track, in result order.
func (p plan) applicable() []string {
	out := []string{anSilence, anClipping}
	if p.hasSemantic {
		out = append(out, anTokenLoops)
	}
	return append(out, anSpectrogram, anTempo)
}

// planFor reads what is present and decides what is missing (D-M2b-12), or everything under force.
func (s *Service) planFor(ctx context.Context, ref library.TrackRef, force bool) (plan, error) {
	rows, sp, err := s.loadRows(ctx, ref.ID)
	if err != nil {
		return plan{}, err
	}
	art, has, err := s.semanticArtifact(ctx, ref.ID)
	if err != nil {
		return plan{}, err
	}
	secs, err := s.o.Library.Sections(ctx, ref.ID)
	if err != nil {
		return plan{}, err
	}
	p := plan{hasSemantic: has, art: art, secs: secs, overlay: buildOverlay(secs, ref.DurationS)}
	_, haveSil := rows[anSilence]
	_, haveClip := rows[anClipping]
	_, haveTok := rows[anTokenLoops]
	_, haveTempo := rows[anTempo]
	p.silence, p.clipping = force || !haveSil, force || !haveClip
	p.tokens = has && (force || !haveTok)
	p.tempo = force || !haveTempo
	p.spectro = force || !sp.current() || sp.Params.OverlaySHA != spectro.OverlaySHA(p.overlay)
	return p, nil
}

// mmss formats seconds as m:ss.
func mmss(sec float64) string {
	n := int(sec)
	return fmt.Sprintf("%d:%02d", n/60, n%60)
}

// buildOverlay is the text drawn on the spectrogram: a header, and either the timed sections with
// their lyric lines or the structure line. Line timing is never claimed (D-M2b-9).
func buildOverlay(secs library.Sections, durationS float64) spectro.Overlay {
	o := spectro.Overlay{Header: spectro.Def + " | " + mmss(durationS) + " | "}
	if !secs.Timed {
		o.Header += "sections: unknown; structure only"
		o.Structure = secs.Structure
		return o
	}
	o.Header += "sections: " + secs.Source
	if secs.Confidence != nil {
		o.Header += fmt.Sprintf(" (confidence %.1f)", *secs.Confidence)
	}
	o.Header += "; lyric lines listed per section, not timed"
	for _, t := range secs.Timeline {
		o.Timed = append(o.Timed, spectro.Section{Label: t.Label, StartS: t.StartS, EndS: t.EndS, Lines: t.Lines})
	}
	return o
}

// results are what one run computed.
type results struct {
	silence  *analysis.Silence
	clipping *analysis.Clipping
	tokens   *tokenMetrics
	picture  *picture
	tempo    *analysis.Tempo
	failed   map[string]string
}

type tokenMetrics struct {
	ArtifactSHA256 string          `json:"artifact_sha256"`
	DType          string          `json:"dtype"`
	Shape          []int           `json:"shape"`
	TimeAxis       int             `json:"time_axis"`
	Streams        []tokens.Stream `json:"streams"`
}

type picture struct {
	sha, rel string
	bytes    int64
	params   spectroParams
}

// AnalyzeTrack runs the missing Tier A analyzers of one track (C51.1).
func (s *Service) AnalyzeTrack(ctx context.Context, trackID string, o AnalyzeOptions) (Outcome, error) {
	out := Outcome{TrackID: trackID}
	if err := s.wired(); err != nil {
		return out, err
	}
	if s.tierAMissing() {
		out.Failed = map[string]string{"tier_a": codeToolMissing}
		return out, nil
	}
	refs, err := s.o.Library.TrackRefs(ctx, []string{trackID})
	if err != nil {
		return out, err
	}
	ref, ok := refs[trackID]
	if !ok {
		return out, library.ErrNotFound
	}
	p, err := s.planFor(ctx, ref, o.Force)
	if err != nil {
		return out, err
	}
	return s.run(ctx, ref, p)
}

func (s *Service) run(ctx context.Context, ref library.TrackRef, p plan) (Outcome, error) {
	out := Outcome{TrackID: ref.ID}
	need := p.needed()
	for _, n := range p.applicable() {
		if !slices.Contains(need, n) {
			out.Skipped = append(out.Skipped, n)
		}
	}
	if !p.any() {
		return out, nil
	}
	res, err := s.compute(ctx, ref, p)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := s.store(ctx, ref.ID, res); err != nil {
		return out, err
	}
	for _, n := range need {
		if _, bad := res.failed[n]; !bad {
			out.Ran = append(out.Ran, n)
		}
	}
	if len(res.failed) > 0 {
		out.Failed = res.failed
	}
	return out, nil
}

// compute runs the analyzers in a private work dir that holds a hard link to the master.
func (s *Service) compute(ctx context.Context, ref library.TrackRef, p plan) (results, error) {
	res := results{failed: map[string]string{}}
	failAll := func(code string) results {
		for _, n := range p.needed() {
			res.failed[n] = code
		}
		return res
	}
	abs, err := s.absData()
	if err != nil {
		return failAll(codeIO), nil
	}
	work := path.Join("incoming", "ears-"+uuid.NewV7().String())
	if err := s.o.Data.MkdirAll("incoming", 0o750); err != nil {
		return failAll(codeIO), nil
	}
	if err := s.o.Data.Mkdir(work, 0o700); err != nil {
		return failAll(codeIO), nil
	}
	defer func() { _ = s.o.Data.RemoveAll(work) }()
	masterName := "master" + path.Ext(ref.MasterPath)
	if err := s.o.Data.Link(ref.MasterPath, path.Join(work, masterName)); err != nil {
		return failAll(codeIO), nil
	}
	workAbs := filepath.Join(abs, filepath.FromSlash(work))
	master := filepath.Join(workAbs, masterName)

	if p.silence || p.clipping {
		sil, clip, err := s.o.Analyzer.Signal(ctx, master, ref.SampleRate, ref.DurationS)
		switch {
		case ctx.Err() != nil:
			return res, ctx.Err()
		case err != nil:
			if p.silence {
				res.failed[anSilence] = codeToolFailed
			}
			if p.clipping {
				res.failed[anClipping] = codeToolFailed
			}
		default:
			res.silence, res.clipping = &sil, &clip
		}
	}
	if p.tokens {
		m, code := s.tokenLoops(ctx, p.art, ref.DurationS)
		if code != "" {
			res.failed[anTokenLoops] = code
		} else {
			res.tokens = m
		}
	}
	if p.spectro {
		pic, code := s.render(ctx, ref, p, workAbs, master, work)
		switch {
		case ctx.Err() != nil:
			return res, ctx.Err()
		case code != "":
			res.failed[anSpectrogram] = code
		default:
			res.picture = pic
		}
	}
	if p.tempo {
		tm, err := s.o.Analyzer.Tempo(ctx, master, ref.DurationS)
		switch {
		case ctx.Err() != nil:
			return res, ctx.Err()
		case err != nil:
			res.failed[anTempo] = codeToolFailed
		default:
			if tm.Candidates == nil {
				tm.Candidates = []analysis.TempoCandidate{}
			}
			res.tempo = &tm
		}
	}
	return res, nil
}

// tokenLoops reads semantic.npy through the data root and measures it.
func (s *Service) tokenLoops(ctx context.Context, art library.ArtifactRef, durationS float64) (*tokenMetrics, string) {
	f, err := s.o.Data.Open(art.Path)
	if err != nil {
		return nil, codeIO
	}
	defer func() { _ = f.Close() }()
	arr, err := npy.Read(f, maxSemanticBytes)
	switch {
	case errors.Is(err, npy.ErrUnsupported), errors.Is(err, npy.ErrCorrupt):
		return nil, codeNPY
	case err != nil:
		return nil, codeIO
	}
	rep, err := tokens.AnalyzeContext(ctx, arr, durationS)
	if err != nil {
		return nil, codeNPY
	}
	return &tokenMetrics{ArtifactSHA256: art.SHA256, DType: rep.DType, Shape: rep.Shape, TimeAxis: rep.TimeAxis, Streams: rep.Streams}, ""
}

// render draws the spectrogram and places it content-addressed under derived/ (read-only).
func (s *Service) render(ctx context.Context, ref library.TrackRef, p plan, workAbs, master, work string) (*picture, string) {
	r, err := spectro.Render(ctx, s.o.Analyzer, spectro.Input{MasterPath: master, WorkDir: workAbs, SampleRate: ref.SampleRate, DurationS: ref.DurationS, Overlay: p.overlay})
	if err != nil {
		if code := spectro.Code(err); code != "" {
			return nil, code
		}
		return nil, codeToolFailed
	}
	sum := sha256.Sum256(r.JPEG)
	sha := hex.EncodeToString(sum[:])
	rel := path.Join("derived", sha[:2], sha+".spectro.jpg")
	tmp := path.Join(work, "spectro.jpg")
	if err := s.o.Data.WriteFile(tmp, r.JPEG, 0o600); err != nil {
		return nil, codeIO
	}
	if err := s.o.Data.Chmod(tmp, 0o444); err != nil {
		return nil, codeIO
	}
	if err := s.o.Data.MkdirAll(path.Dir(rel), 0o750); err != nil {
		return nil, codeIO
	}
	if err := s.o.Data.Link(tmp, rel); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, codeIO
	}
	info := &overlayInfo{Timed: p.secs.Timed, Confidence: p.secs.Confidence}
	if p.secs.Timed {
		src := p.secs.Source
		info.Source = &src
	} else {
		info.Confidence = nil
	}
	return &picture{sha: sha, rel: rel, bytes: int64(len(r.JPEG)), params: spectroParams{
		Def: spectro.Def, FFmpeg: strings.TrimPrefix(s.o.Analyzer.ToolVersion(), "ffmpeg "),
		Plot: fmt.Sprintf("%dx%d", r.PlotW, r.PlotH), Quality: r.Quality, Width: r.Width, Height: r.Height,
		Geometry:   spectroGeometry{X0: r.Geometry.X0, Y0: r.Geometry.Y0, W: r.Geometry.W, H: r.Geometry.H, DurationS: r.Geometry.DurationS},
		OverlaySHA: r.OverlaySHA, FreqScale: "log", Overlay: info,
	}}, ""
}

// store commits everything that succeeded in one write transaction (C44 write SQL).
func (s *Service) store(ctx context.Context, trackID string, res results) error {
	if res.silence == nil && res.clipping == nil && res.tokens == nil && res.picture == nil && res.tempo == nil {
		return nil
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ears: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	tool := s.o.Analyzer.ToolVersion()
	put := func(analyzer, version string, v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, ?, ?, 'A', ?, ?)
			ON CONFLICT(track_id, analyzer, version) DO NOTHING`, trackID, analyzer, version, string(raw), now)
		return err
	}
	if res.silence != nil {
		if err := put(anSilence, analysis.SilenceDef+" "+tool, res.silence); err != nil {
			return fmt.Errorf("ears: store silence: %w", err)
		}
	}
	if res.clipping != nil {
		if err := put(anClipping, analysis.ClippingDef+" "+tool, res.clipping); err != nil {
			return fmt.Errorf("ears: store clipping: %w", err)
		}
	}
	if res.tokens != nil {
		if err := put(anTokenLoops, tokens.Version(), res.tokens); err != nil {
			return fmt.Errorf("ears: store token loops: %w", err)
		}
	}
	if res.tempo != nil {
		if err := put(anTempo, analysis.TempoDef+" "+tool, res.tempo); err != nil {
			return fmt.Errorf("ears: store tempo: %w", err)
		}
	}
	if pic := res.picture; pic != nil {
		params, err := json.Marshal(pic.params)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO renditions(track_id, kind, sha256, bytes, path, params_json, created_at) VALUES (?, 'spectrogram', ?, ?, ?, ?, ?)
			ON CONFLICT(track_id, kind) DO UPDATE SET sha256=excluded.sha256, bytes=excluded.bytes, path=excluded.path, params_json=excluded.params_json, created_at=excluded.created_at`,
			trackID, pic.sha, pic.bytes, pic.rel, string(params), now); err != nil {
			return fmt.Errorf("ears: store spectrogram: %w", err)
		}
	}
	return tx.Commit()
}
