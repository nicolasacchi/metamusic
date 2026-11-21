package transform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"time"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/score"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
	"github.com/nicolasacchi/metamusic/internal/score/smf"
	"github.com/nicolasacchi/metamusic/internal/score/synth"
)

// The T1b score pipeline (C355): ABC text to a score-faithful Take. The pure-Go steps (parse, events, SMF) and fluidsynth run in
// the parent (D-DESK-7); the render child runs kind score_render (identity plus the common safety finish), so pcm_sha256 stays
// the oracle. The same pipeline serves the Desk's auditions (RenderScore) and the transform path (attempt, Verify).

// ScoreSupport is what a score render needs beyond the media tools: the synth, its identity, the configured soundfonts and the
// reader of a take's score text. A nil Options.Score means T1b is not built into this service (create answers 503).
type ScoreSupport struct {
	Synth synth.Synth
	// Info is the engine identity read at startup (name and the first line of `fluidsynth --version`).
	Info  synth.Info
	Fonts *synth.Set
	// ScoreABC reads the score.abc of a take and its recorded sha256 (library.Service.ScoreABC). Create and run use it for
	// from_track_id; an estimate never does (D-T1-6).
	ScoreABC func(ctx context.Context, trackID string) (text []byte, sha256 string, ok bool, err error)
}

// ErrBusy is Exclusive's answer when the render slot stayed taken for the whole wait.
var ErrBusy = errors.New("transform: the render slot is busy")

// ScoreJob is one score render. Dialect, Chords, TailS, Gain, TargetLUFS and Soundfont are the resolved values of the spec.
// The optional fields default from the service's configuration.
type ScoreJob struct {
	Score      []byte
	Dialect    abc.Dialect
	Chords     abc.ChordMode
	BPM        *float64
	TailS      float64
	Gain       float64
	TargetLUFS float64
	Soundfont  string

	Rate     int     // 0 = 48000
	MaxOutS  float64 // 0 = the configured transform output limit
	MaxCPUMS int64   // 0 = the configured transform CPU limit
	MemMB    int     // 0 = the estimate for the rendered length
}

// ScoreOut is the result of RenderScore. The work dir holds score.abc, score.mid, out.pcm24 (24-bit stereo) and report.json.
type ScoreOut struct {
	PCMSHA256       string
	Frames          int64
	Seconds         float64
	Report          json.RawMessage
	Synth           synth.Info
	SoundfontSHA256 string
	CPU             time.Duration
}

// ScoreError is a failed score render: a stable code of codes.go and a message safe to store or show.
type ScoreError struct{ Code, Message string }

func (e *ScoreError) Error() string { return e.Code + ": " + e.Message }

// scoreAvailable is the gate of estimate, create and capabilities: a readable configured soundfont and the tool.
func (s *Service) scoreAvailable() error {
	sup := s.o.Score
	if sup == nil || sup.Fonts == nil || len(sup.Fonts.Names()) == 0 {
		return unavailable("soundfont")
	}
	if !s.tools().Fluidsynth || sup.Synth == nil {
		return unavailable("fluidsynth")
	}
	return nil
}

// scoreInvalid is 422 SCORE_INVALID for a score the parser or the event builder refuses; the details name the rule and the
// position, never the text.
func scoreInvalid(err error) error {
	d := map[string]any{"rule": scoreRule(err)}
	var se *abc.SyntaxError
	if errors.As(err, &se) {
		d["line"], d["col"] = se.Line, se.Col
	}
	return problem.E(422, ErrScoreInvalid, "the score cannot be rendered: "+scoreRule(err), d)
}

func scoreRule(err error) string {
	var se *abc.SyntaxError
	switch {
	case errors.As(err, &se):
		return se.Rule
	case errors.Is(err, score.ErrTooLong):
		return "too_long"
	case errors.Is(err, score.ErrTooManyVoices):
		return "too_many_voices"
	case errors.Is(err, score.ErrTooManyEvents):
		return "too_many_events"
	}
	return "invalid"
}

// loadScore parses the score text and builds its events. It is the one definition of "the score" for the estimate, the render
// and the Desk, so a length promised is the length rendered. bpm, when given, replaces the tempo: every tempo of the map is
// scaled so the first one reads bpm quarter notes a minute.
func loadScore(text []byte, dialect abc.Dialect, chords abc.ChordMode, bpm *float64) (*score.Score, error) {
	if len(text) > MaxScoreBytes {
		return nil, &abc.SyntaxError{Line: 1, Col: 1, Rule: RuleScoreTooLarge, Msg: "score exceeds the size limit"}
	}
	t, err := abc.Parse(text, abc.Options{Dialect: dialect})
	if err != nil {
		return nil, err
	}
	sc, err := t.Score(abc.EventOptions{Chords: chords})
	if err != nil {
		return nil, err
	}
	if sc.EndTick() <= 0 {
		return nil, &abc.SyntaxError{Line: 1, Col: 1, Rule: "empty", Msg: "the score has no notes"}
	}
	if bpm != nil {
		applyBPM(sc, *bpm)
	}
	return sc, nil
}

// applyBPM scales the tempo map so the first entry is bpm quarter notes a minute.
func applyBPM(sc *score.Score, bpm float64) {
	if bpm <= 0 || len(sc.Tempos) == 0 || sc.Tempos[0].USPerQ == 0 {
		return
	}
	f := (6e7 / float64(sc.Tempos[0].USPerQ)) / bpm
	for i := range sc.Tempos {
		us := math.Round(float64(sc.Tempos[i].USPerQ) * f)
		sc.Tempos[i].USPerQ = uint32(min(max(us, 1), 0xFFFFFF)) // #nosec G115 -- clamped to 24 bits
	}
}

func scoreSeconds(sc *score.Score) float64 { return sc.SecondsAt(sc.EndTick()) }

// scoreVoices is the score-faithfulness report of transform.json (T1 C208.6): per voice the note count and the first and last
// onset in seconds from the tempo map, so the audio can be compared with the score without trusting the synth.
func scoreVoices(sc *score.Score) json.RawMessage {
	type voice struct {
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		Program int     `json:"program"`
		Notes   int     `json:"notes"`
		FirstS  float64 `json:"first_onset_s"`
		LastS   float64 `json:"last_onset_s"`
		Channel int     `json:"channel"`
	}
	out := make([]voice, 0, len(sc.Voices))
	for _, v := range sc.Voices {
		x := voice{ID: v.ID, Name: v.Name, Program: v.Program, Channel: v.Channel, Notes: len(v.Notes)}
		if len(v.Notes) > 0 {
			first, last := v.Notes[0].Tick, v.Notes[0].Tick
			for _, n := range v.Notes {
				first, last = min(first, n.Tick), max(last, n.Tick)
			}
			x.FirstS, x.LastS = round(sc.SecondsAt(first), 3), round(sc.SecondsAt(last), 3)
		}
		out = append(out, x)
	}
	b, _ := json.Marshal(out)
	return b
}

// scoreResolved is the outcome of resolving a score_render request at create time: the params with every default written out,
// the spec's synth and score records, the optional score_source input and the output length.
type scoreResolved struct {
	Params   json.RawMessage
	Synth    *SynthRef
	Score    *ScoreRef
	Source   *inputInfo
	OutS     float64
	Warnings []Warning
}

// resolveScore is step 3 and 4 of the create order for a score render. It reads database rows only (an estimate reads no file):
// an inline score is parsed from the request, a from_track_id score is sized from its scores row.
func (s *Service) resolveScore(ctx context.Context, q queryer, validated json.RawMessage) (scoreResolved, error) {
	var res scoreResolved
	var p map[string]any
	if err := json.Unmarshal(validated, &p); err != nil {
		return res, fmt.Errorf("transform: resolved score params: %w", err)
	}
	var src struct {
		ABC  string `json:"abc"`
		From string `json:"from_track_id"`
	}
	sm, _ := json.Marshal(p["score"])
	_ = json.Unmarshal(sm, &src)
	m := paramsMap(validated)
	fonts := s.o.Score.Fonts

	// the soundfont: a configured name, the first one when none is named
	name, _ := p["soundfont"].(string)
	if name == "" {
		name = fonts.Names()[0]
		res.Warnings = append(res.Warnings, Warning{Code: WarnSoundfontDefaultUsed, Field: "soundfont"})
	}
	_, sfSHA, _, ok := fonts.Lookup(name)
	if !ok {
		return res, problem.E(422, ErrSoundfontUnknown, "the soundfont is not configured", map[string]any{"soundfont": name, "configured": fonts.Names()})
	}
	var bpm *float64
	if v := rawFloat(m, "bpm"); v != nil {
		bpm = v
	}
	chords := abc.ChordMode(paramString(m, "chords"))
	tail := paramFloat(m, "tail_s", scoreTailDefault)
	dialect, _ := p["dialect"].(string)

	var sec float64
	switch {
	case src.ABC != "":
		if dialect == "" {
			dialect = dialectStandard
		}
		sc, err := loadScore([]byte(src.ABC), abc.Dialect(dialect), chords, bpm)
		if err != nil {
			return res, scoreInvalid(err)
		}
		sec = scoreSeconds(sc)
		sum := sha256.Sum256([]byte(src.ABC))
		res.Score = &ScoreRef{SHA256: hex.EncodeToString(sum[:]), Bytes: len(src.ABC), Dialect: dialect}
	default:
		in, row, err := s.scoreSource(ctx, q, src.From)
		if err != nil {
			return res, err
		}
		if dialect == "" {
			dialect = dialectStandard
			if row.engine == "yue2" {
				dialect = dialectYuE2
			}
		}
		if sec = row.seconds(bpm); sec <= 0 {
			return res, scoreInvalid(&abc.SyntaxError{Rule: "empty"})
		}
		id := src.From
		res.Score = &ScoreRef{SHA256: row.sha, SourceTrackID: &id, Dialect: dialect}
		res.Source = &in
	}
	p["dialect"], p["soundfont"] = dialect, name
	b, err := marshalResolved(p)
	if err != nil {
		return res, err
	}
	res.Params = b
	res.Synth = &SynthRef{Name: name, SoundfontSHA: sfSHA, Engine: "fluidsynth"}
	res.OutS = sec + tail
	return res, nil
}

// scoreRow is what the estimate knows of a take's score without opening a file.
type scoreRow struct {
	engine, sha, meter string
	bars               int
	bpm, secPerBar     sql.NullFloat64
}

// seconds is the length of the take's score: bars x seconds_per_bar when timed, else the bars at the default tempo; a tempo
// override rescales either.
func (r scoreRow) seconds(override *float64) float64 {
	if r.secPerBar.Valid && r.secPerBar.Float64 > 0 {
		sec := float64(r.bars) * r.secPerBar.Float64
		if override != nil {
			from := 120.0 // an untempoed score runs at the default tempo
			if r.bpm.Valid && r.bpm.Float64 > 0 {
				from = r.bpm.Float64
			}
			sec *= from / *override
		}
		return sec
	}
	bpm := 120.0
	switch {
	case override != nil:
		bpm = *override
	case r.bpm.Valid && r.bpm.Float64 > 0:
		bpm = r.bpm.Float64
	}
	num, den := 4, 4
	if _, err := fmt.Sscanf(r.meter, "%d/%d", &num, &den); err != nil || num <= 0 || den <= 0 {
		num, den = 4, 4
	}
	return float64(r.bars) * float64(num) * 4 / float64(den) * 60 / bpm
}

// scoreSource reads the take a from_track_id score comes from: the score_source annotation and the row that sizes the render.
func (s *Service) scoreSource(ctx context.Context, q queryer, trackID string) (inputInfo, scoreRow, error) {
	in := inputInfo{Ref: InputRef{Role: "score_source", TrackID: trackID}}
	var row scoreRow
	var sha, meter sql.NullString
	var bars sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT t.title, t.master_sha256, r.engine, sc.abc_sha256, sc.bars, sc.meter, sc.bpm, sc.seconds_per_bar
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id LEFT JOIN scores sc ON sc.track_id = t.id WHERE t.id = ?`, trackID).
		Scan(&in.Title, &in.Ref.MasterSHA256, &row.engine, &sha, &bars, &meter, &row.bpm, &row.secPerBar)
	if errors.Is(err, sql.ErrNoRows) {
		return in, row, notFound(trackID)
	}
	if err != nil {
		return in, row, fmt.Errorf("transform: score source: %w", err)
	}
	if !sha.Valid {
		return in, row, problem.E(422, ErrScoreInvalid, "the take has no score", map[string]any{"field": "score.from_track_id", "rule": "no_score"})
	}
	row.sha, row.bars, row.meter = sha.String, int(bars.Int64), meter.String
	return in, row, nil
}

// ---- the pipeline ----

// RenderScore runs the whole pipeline in the existing work dir workRel (relative to the data root; created 0700 when missing): parse,
// events, SMF, fluidsynth, exact decode, and the render child. It is for the Desk's auditions; the transform runner shares the
// same steps through scoreStaged. progress may be nil. Errors are *ScoreError.
func (s *Service) RenderScore(ctx context.Context, j ScoreJob, workRel string, progress ChildProgress) (ScoreOut, error) {
	var acct cpuAcct
	out, _, f := s.renderScore(ctx, j, &staged{rel: workRel}, &acct, progress, nil)
	if f != nil {
		return ScoreOut{}, &ScoreError{Code: f.code, Message: s.clean(f.msg)}
	}
	return out, nil
}

// renderScore is the pipeline. before runs after the decode and before the child (the runner's after_decode fault point).
func (s *Service) renderScore(ctx context.Context, j ScoreJob, st *staged, acct *cpuAcct, progress ChildProgress, before func() *runFailure) (ScoreOut, childReport, *runFailure) {
	var zero childReport
	workRel := st.rel
	sup := s.o.Score
	if sup == nil || sup.Synth == nil || sup.Fonts == nil {
		return ScoreOut{}, zero, fail(ErrSynthFailed, "the score renderer is not available")
	}
	sfPath, sfSHA, _, ok := sup.Fonts.Lookup(j.Soundfont)
	if !ok {
		return ScoreOut{}, zero, fail(ErrSoundfontUnknown, "the soundfont is not configured")
	}
	rate := j.Rate
	if rate == 0 {
		rate = 48000
	}
	sc, err := loadScore(j.Score, j.Dialect, j.Chords, j.BPM)
	if err != nil {
		return ScoreOut{}, zero, fail(ErrScoreInvalid, "the score cannot be rendered: %s", scoreRule(err))
	}
	secs := scoreSeconds(sc)
	maxOut := j.MaxOutS
	if maxOut == 0 {
		maxOut = float64(s.o.Config.TransformMaxOutS)
	}
	if maxOut > 0 && secs+j.TailS > maxOut {
		return ScoreOut{}, zero, fail(ErrInvalidTransform, "the render would be %.0f s, past the %.0f s limit", secs+j.TailS, maxOut)
	}
	if err := ctx.Err(); err != nil {
		return ScoreOut{}, zero, fail(ErrInterrupted, "interrupted")
	}
	if err := s.o.Data.MkdirAll(workRel, 0o700); err != nil {
		return ScoreOut{}, zero, fail(ErrTransformFailed, "the work directory could not be created")
	}
	abs := filepath.Join(s.o.DataDir, filepath.FromSlash(workRel))
	st.abs, st.rate = abs, rate
	st.voices = scoreVoices(sc)
	if err := s.o.Data.WriteFile(path.Join(workRel, "score.abc"), j.Score, 0o600); err != nil {
		return ScoreOut{}, zero, fail(ErrTransformFailed, "score.abc could not be written")
	}
	if err := s.o.Data.WriteFile(path.Join(workRel, "score.mid"), smf.Write(sc, j.TailS), 0o600); err != nil {
		return ScoreOut{}, zero, fail(ErrTransformFailed, "score.mid could not be written")
	}
	t0 := time.Now()
	info, err := sup.Synth.Render(ctx, synth.Request{SMF: filepath.Join(abs, "score.mid"), Soundfont: sfPath, Out: filepath.Join(abs, "out.wav"),
		Rate: rate, Gain: j.Gain, Timeout: synth.TimeoutFor(secs)})
	acct.add(time.Since(t0))
	if err != nil {
		if ctx.Err() != nil {
			return ScoreOut{}, zero, fail(ErrInterrupted, "interrupted")
		}
		var re *synth.RenderError
		if errors.As(err, &re) && errors.Is(re, context.DeadlineExceeded) {
			return ScoreOut{}, zero, fail(ErrTimeout, "the synthesizer ran past its time limit")
		}
		return ScoreOut{}, zero, fail(ErrSynthFailed, "the synthesizer failed")
	}
	d, err := s.media().Decode(ctx, filepath.Join(abs, "out.wav"), filepath.Join(abs, "out.raw"))
	acct.add(d)
	if err != nil {
		if ctx.Err() != nil {
			return ScoreOut{}, zero, fail(ErrInterrupted, "interrupted")
		}
		return ScoreOut{}, zero, fail(ErrTransformFailed, "decode failed: %v", err)
	}
	_ = s.o.Data.Remove(path.Join(workRel, "out.wav"))
	fi, err := s.o.Data.Stat(path.Join(workRel, "out.raw"))
	if err != nil || fi.Size() == 0 || fi.Size()%8 != 0 {
		return ScoreOut{}, zero, fail(ErrSynthFailed, "the synthesizer produced no usable audio")
	}
	frames := fi.Size() / 8 // fluidsynth writes stereo; the decode keeps it exact
	if err := s.o.Data.Rename(path.Join(workRel, "out.raw"), path.Join(workRel, inFile(0))); err != nil {
		return ScoreOut{}, zero, fail(ErrTransformFailed, "decode output could not be placed")
	}
	maxCPU := j.MaxCPUMS
	if maxCPU == 0 {
		maxCPU = int64(s.o.Config.TransformMaxCPUS) * 1000
	}
	if maxCPU <= 0 {
		maxCPU = 120_000
	}
	mem := j.MemMB
	if mem == 0 {
		mem = int(MemoryMB(frames, frames, 1))
	}
	outS := float64(frames) / float64(rate)
	job := jobDoc{V: 1, Kind: KindScoreRender, Rate: rate, Params: json.RawMessage(`{}`),
		Inputs:   []jobInput{{Role: "source", File: inFile(0), Frames: frames}},
		UnitsMax: unitsBudget(maxCPU, outS, outS), TargetLUFS: j.TargetLUFS, Out: outFile, Report: reportFile}
	jb, err := json.Marshal(job)
	if err != nil {
		return ScoreOut{}, zero, fail(ErrTransformFailed, "job.json could not be built")
	}
	if err := s.o.Data.WriteFile(path.Join(workRel, jobFile), jb, 0o600); err != nil {
		return ScoreOut{}, zero, fail(ErrTransformFailed, "job.json could not be written")
	}
	if before != nil {
		if f := before(); f != nil {
			return ScoreOut{}, zero, f
		}
	}
	if f := s.render(ctx, tRow{MemMBEst: mem, MaxCPUMS: maxCPU}, st, acct, progress); f != nil {
		return ScoreOut{}, zero, f
	}
	st.synthVersion = info.Version
	rep, f := s.readReport(st)
	if f != nil {
		return ScoreOut{}, zero, f
	}
	raw, _ := s.o.Data.ReadFile(path.Join(workRel, reportFile))
	return ScoreOut{PCMSHA256: rep.PCMSHA256, Frames: rep.Frames, Seconds: float64(rep.Frames) / float64(rate), Report: raw,
		Synth: info, SoundfontSHA256: sfSHA, CPU: acct.d}, rep, nil
}

// scoreStaged renders the score of a staged score_render transform (the runner's attempt and Verify): it rebuilds the job from the
// spec, checks that the score and the soundfont are still the ones the spec froze, and runs the pipeline.
func (s *Service) scoreStaged(ctx context.Context, st *staged, acct *cpuAcct, maxCPUMS int64, memMB int, progress ChildProgress, before func() *runFailure) (ScoreOut, childReport, *runFailure) {
	var zero childReport
	sup := s.o.Score
	if sup == nil || sup.Fonts == nil {
		return ScoreOut{}, zero, fail(ErrSynthFailed, "the score renderer is not available")
	}
	sp := st.spec
	if sp.Score == nil || sp.Synth == nil {
		return ScoreOut{}, zero, fail(ErrInvalidTransform, "the stored spec has no score or synth record")
	}
	m := paramsMap(sp.Params)
	var src struct {
		ABC  string `json:"abc"`
		From string `json:"from_track_id"`
	}
	_ = json.Unmarshal(m["score"], &src)
	text := []byte(src.ABC)
	if src.ABC == "" {
		if sup.ScoreABC == nil || src.From == "" {
			return ScoreOut{}, zero, fail(ErrInvalidTransform, "the stored spec names no score")
		}
		b, sha, ok, err := sup.ScoreABC(ctx, src.From)
		if err != nil {
			return ScoreOut{}, zero, fail(ErrTransformFailed, "the score of the source take could not be read")
		}
		if !ok || sha != sp.Score.SHA256 {
			return ScoreOut{}, zero, fail(ErrInputChanged, "the score of the source take is gone or changed since the transform was created")
		}
		text = b
	}
	if sum := sha256.Sum256(text); hex.EncodeToString(sum[:]) != sp.Score.SHA256 {
		return ScoreOut{}, zero, fail(ErrInputChanged, "the score no longer matches the spec")
	}
	name := paramString(m, "soundfont")
	if _, sha, _, ok := sup.Fonts.Lookup(name); !ok || sha != sp.Synth.SoundfontSHA {
		return ScoreOut{}, zero, fail(ErrInputChanged, "the soundfont is gone or changed since the transform was created")
	}
	j := ScoreJob{Score: text, Dialect: abc.Dialect(paramString(m, "dialect")), Chords: abc.ChordMode(paramString(m, "chords")), BPM: rawFloat(m, "bpm"),
		TailS: paramFloat(m, "tail_s", scoreTailDefault), Gain: paramFloat(m, "gain", scoreGainDefault), TargetLUFS: sp.TargetLUFS, Soundfont: name,
		Rate: sp.SampleRate, MaxCPUMS: maxCPUMS, MemMB: memMB}
	return s.renderScore(ctx, j, st, acct, progress, before)
}

// scoreArtifacts are the work files a score render keeps beside request.json and transform.json.
var scoreArtifacts = []string{"score.abc", "score.mid"}

// synthDiff names why a recorded synth cannot be reproduced here ("" when it can): the soundfont must be configured with the
// recorded hash and the fluidsynth major.minor must match.
func (s *Service) synthDiff(rec *synthDoc) string {
	sup := s.o.Score
	if sup == nil || sup.Fonts == nil || sup.Synth == nil {
		return fmt.Sprintf("the recorded synth %s %s is not available in this build", rec.Name, majorMinor(rec.Version))
	}
	found := false
	for _, n := range sup.Fonts.Names() {
		if _, sha, _, ok := sup.Fonts.Lookup(n); ok && sha == rec.SoundfontSHA {
			found = true
		}
	}
	if !found {
		return "the recorded soundfont is not configured"
	}
	if got, want := majorMinor(sup.Info.Version), majorMinor(rec.Version); got != want {
		return fmt.Sprintf("the synth version differs (recorded %s, running %s)", want, got)
	}
	return ""
}

// ---- Exclusive ----

// slotCh is the one-slot semaphore the runner and the Desk's auditions share, so an audition never runs beside a transform.
func (s *Service) slotCh() chan struct{} {
	s.run.slotOnce.Do(func() { s.run.slot = make(chan struct{}, 1) })
	return s.run.slot
}

// takeSlot waits for the slot until ctx ends.
func (s *Service) takeSlot(ctx context.Context) (release func(), err error) {
	select {
	case s.slotCh() <- struct{}{}:
		return func() { <-s.slotCh() }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Exclusive runs fn while holding the render slot, the one the transform runner holds around every attempt. It waits at most
// wait for the slot and then answers ErrBusy; fn is never run without the slot.
func (s *Service) Exclusive(ctx context.Context, wait time.Duration, fn func(context.Context) error) error {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case s.slotCh() <- struct{}{}:
	case <-t.C:
		return ErrBusy
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.slotCh() }()
	return fn(ctx)
}
