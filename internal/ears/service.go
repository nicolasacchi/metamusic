package ears

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// wired reports whether the service has what it reads from. An unwired Service (Options{}, as
// handlers tests build it) answers errors.ErrUnsupported, which the API maps to 501 NOT_IMPLEMENTED,
// exactly like the stage-1 stub did.
func (s *Service) wired() error {
	if s.o.Store == nil || s.o.Library == nil {
		return errors.ErrUnsupported
	}
	return nil
}

// UseJudgments wires the duels service after both are built (app.Build creates ears first).
func (s *Service) UseJudgments(j Judgments) { s.o.Judgments = j }

// Analyzer names in the order every result lists them.
const (
	anSilence     = "silence"
	anClipping    = "clipping"
	anTokenLoops  = "token_loops"
	anSpectrogram = "spectrogram"
	anLoudness    = "loudness"
	anSpectrum    = "spectrum"
	anTempo       = "tempo"
)

const (
	codeToolMissing = "TOOL_MISSING"
	codeToolFailed  = "TOOL_FAILED"
	codeIO          = "IO"
	codeNPY         = "NPY_UNSUPPORTED"
)

// defs maps an analyzer to its current definition identifier (D-M2b-12: a row is current when its
// version starts with "<definition> ").
var defs = map[string]string{
	anLoudness:   analysis.LoudnessDef,
	anSpectrum:   analysis.SpectrumDef,
	anSilence:    analysis.SilenceDef,
	anClipping:   analysis.ClippingDef,
	anTokenLoops: analysis.TokenLoopDef,
	anTempo:      analysis.TempoDef,
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

func (s *Service) now() string { return s.o.Now().UTC().Format(time.RFC3339) }

// absData is the absolute data directory the Tier A work paths are built under.
func (s *Service) absData() (string, error) { return filepath.Abs(s.o.DataDir) }

// missingTierA reports whether Tier A cannot run, and the capability error.
func (s *Service) missingTierA() error {
	if s.o.Missing != nil {
		return s.o.Missing
	}
	if s.o.Analyzer == nil {
		return fmt.Errorf("%w: tier A analyzer", analysis.ErrToolMissing)
	}
	return nil
}

// tierAMissing reports whether Tier A cannot run.
func (s *Service) tierAMissing() bool { return s.missingTierA() != nil }

// analysisRow is one stored analyses row.
type analysisRow struct {
	Version, Tier, Metrics, CreatedAt string
}

// overlayInfo records what the stored picture drew (rendition params).
type overlayInfo struct {
	Timed      bool     `json:"timed"`
	Source     *string  `json:"source"`
	Confidence *float64 `json:"confidence"`
}

type spectroGeometry struct {
	X0        int     `json:"x0"`
	Y0        int     `json:"y0"`
	W         int     `json:"w"`
	H         int     `json:"h"`
	DurationS float64 `json:"duration_s"`
}

// spectroParams is the rendition params_json of a spectrogram (C50.3).
type spectroParams struct {
	Def        string          `json:"def"`
	FFmpeg     string          `json:"ffmpeg"`
	Plot       string          `json:"plot"`
	Quality    int             `json:"quality"`
	Width      int             `json:"width"`
	Height     int             `json:"height"`
	Geometry   spectroGeometry `json:"geometry"`
	OverlaySHA string          `json:"overlay_sha256"`
	FreqScale  string          `json:"freq_scale"`
	Overlay    *overlayInfo    `json:"overlay,omitempty"`
}

// spectroRow is the stored spectrogram rendition of a track.
type spectroRow struct {
	SHA256, Path string
	Bytes        int64
	Params       spectroParams
}

// current reports whether the picture was made by the current definition.
func (r *spectroRow) current() bool { return r != nil && r.Params.Def == analysis.SpectroDef }

// loadRows reads, per analyzer, the row with the newest created_at (ties: the greatest version) among
// those whose version starts with the analyzer's current definition, and the spectrogram rendition.
func (s *Service) loadRows(ctx context.Context, trackID string) (map[string]analysisRow, *spectroRow, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT analyzer, version, tier, metrics_json, created_at FROM analyses WHERE track_id = ?`, trackID)
	if err != nil {
		return nil, nil, fmt.Errorf("ears: analyses: %w", err)
	}
	out := map[string]analysisRow{}
	for rows.Next() {
		var an string
		var r analysisRow
		if err := rows.Scan(&an, &r.Version, &r.Tier, &r.Metrics, &r.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("ears: analyses: %w", err)
		}
		def, ok := defs[an]
		if !ok || !strings.HasPrefix(r.Version, def+" ") {
			continue
		}
		if cur, have := out[an]; have && (cur.CreatedAt > r.CreatedAt || (cur.CreatedAt == r.CreatedAt && cur.Version >= r.Version)) {
			continue
		}
		out[an] = r
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	var sp spectroRow
	var params string
	switch err := s.o.Store.R.QueryRowContext(ctx, `SELECT sha256, bytes, path, params_json FROM renditions WHERE track_id = ? AND kind = 'spectrogram'`, trackID).
		Scan(&sp.SHA256, &sp.Bytes, &sp.Path, &params); {
	case err == nil:
		if json.Unmarshal([]byte(params), &sp.Params) != nil {
			sp.Params = spectroParams{} // unreadable params: not a current picture
		}
		return out, &sp, nil
	case errors.Is(err, sql.ErrNoRows):
		return out, nil, nil
	default:
		return nil, nil, fmt.Errorf("ears: spectrogram rendition: %w", err)
	}
}

// hasSemantic reports whether the take kept a semantic.npy; the artifact row is returned when so.
func (s *Service) semanticArtifact(ctx context.Context, trackID string) (library.ArtifactRef, bool, error) {
	a, err := s.o.Library.Artifact(ctx, trackID, "semantic.npy")
	switch {
	case err == nil:
		return a, true, nil
	case errors.Is(err, library.ErrNotFound):
		return library.ArtifactRef{}, false, nil
	}
	return library.ArtifactRef{}, false, err
}

// state is everything the read payloads are built from.
type state struct {
	d           library.TrackDetail
	secs        library.Sections
	rows        map[string]analysisRow
	spectro     *spectroRow
	art         library.ArtifactRef
	hasSemantic bool

	loudness *library.LoudnessOut
	spectrum *library.SpectrumOut
	silence  *SilenceBlock
	clipping *ClippingBlock
	tokens   *TokenLoopsBlock
	tempo    *analysis.Tempo // nil when the analyzer has not run; BPM nil when it found none

	tier    string
	missing []string
	flags   []Flag
}

func decode[T any](row analysisRow, ok bool) *T {
	if !ok {
		return nil
	}
	var v T
	if json.Unmarshal([]byte(row.Metrics), &v) != nil {
		return nil
	}
	return &v
}

// load reads and decodes everything about one track. library.ErrNotFound for an unknown track.
func (s *Service) load(ctx context.Context, trackID string) (*state, error) {
	if err := s.wired(); err != nil {
		return nil, err
	}
	d, err := s.o.Library.Track(ctx, trackID)
	if err != nil {
		return nil, err
	}
	secs, err := s.o.Library.Sections(ctx, trackID)
	if err != nil {
		return nil, err
	}
	rows, sp, err := s.loadRows(ctx, trackID)
	if err != nil {
		return nil, err
	}
	art, has, err := s.semanticArtifact(ctx, trackID)
	if err != nil {
		return nil, err
	}
	st := &state{d: d, secs: secs, rows: rows, spectro: sp, art: art, hasSemantic: has}
	if r, ok := rows[anLoudness]; ok {
		if l := decode[library.LoudnessOut](r, true); l != nil {
			if l.ShortTerm1Hz == nil {
				l.ShortTerm1Hz = []float64{}
			}
			l.Version = r.Version
			st.loudness = l
		}
	}
	if r, ok := rows[anSpectrum]; ok {
		if v := decode[library.SpectrumOut](r, true); v != nil {
			v.Version = r.Version
			st.spectrum = v
		}
	}
	if r, ok := rows[anSilence]; ok {
		if v := decode[SilenceBlock](r, true); v != nil {
			if v.Spans == nil {
				v.Spans = []analysis.Span{}
			}
			v.Version = r.Version
			st.silence = v
		}
	}
	if r, ok := rows[anClipping]; ok {
		if v := decode[ClippingBlock](r, true); v != nil {
			if v.Spans == nil {
				v.Spans = []analysis.Span{}
			}
			v.Version = r.Version
			st.clipping = v
		}
	}
	if r, ok := rows[anTokenLoops]; ok {
		if v := decode[TokenLoopsBlock](r, true); v != nil {
			if v.ArtifactSHA256 == "" {
				v.ArtifactSHA256 = art.SHA256
			}
			if v.Shape == nil {
				v.Shape = []int{}
			}
			v.Version = r.Version
			st.tokens = v
		}
	}
	if r, ok := rows[anTempo]; ok {
		st.tempo = decode[analysis.Tempo](r, true)
	}
	st.tier, st.missing = tierOf(st)
	st.flags = computeFlags(st.flagInput())
	return st, nil
}

// tierOf applies the C51.4 tier rule.
func tierOf(st *state) (string, []string) {
	missing := []string{}
	if st.silence == nil {
		missing = append(missing, anSilence)
	}
	if st.clipping == nil {
		missing = append(missing, anClipping)
	}
	if st.hasSemantic && st.tokens == nil {
		missing = append(missing, anTokenLoops)
	}
	if !st.spectro.current() {
		missing = append(missing, anSpectrogram)
	}
	if len(missing) == 0 {
		return "A", missing
	}
	return "0", missing
}

func (st *state) flagInput() flagInput {
	in := flagInput{Loudness: st.loudness, Silence: st.silence, Clipping: st.clipping, Tokens: st.tokens,
		Timed: st.d.Timed, DurationS: st.d.DurationS, ExpectedS: st.secs.ExpectedDurationS}
	if st.d.Truncated != nil {
		in.TruncatedABC, in.TruncatedSemantic = st.d.Truncated.ABC, st.d.Truncated.Semantic
	}
	if st.spectrum != nil {
		in.BandwidthHz = st.spectrum.BandwidthHz
	}
	return in
}

func (st *state) flagCodes() []string {
	out := make([]string, 0, len(st.flags))
	for _, f := range st.flags {
		out = append(out, f.Code)
	}
	return out
}

// sectionFacts is what the blind-spot list needs to know about section times.
func (st *state) sectionSource() (source string, conf *float64) {
	if !st.d.Timed || len(st.d.Sections) == 0 {
		return "", nil
	}
	return st.d.Sections[0].Source, st.d.Sections[0].Confidence
}

func (st *state) blindFacts(spectrogram bool) analysis.BlindSpotFacts {
	src, conf := st.sectionSource()
	return analysis.BlindSpotFacts{TierA: st.tier == "A", HasSemantic: st.hasSemantic, Timed: st.d.Timed,
		SectionSource: src, Confidence: conf, Spectrogram: spectrogram, Lossy: st.d.Lossy, Tempo: st.tempo != nil && !spectrogram}
}

func (st *state) blindSpots(spectrogram bool) []string {
	return analysis.BlindSpots(st.blindFacts(spectrogram))
}
