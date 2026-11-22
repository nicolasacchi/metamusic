package desk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// Auditions (C358.3, D-DESK-6): a CPU render of a plan's score through the T1b pipeline, kept as a content-addressed MP3 under
// derived/auditions/. An audition is a Desk preview, never a Take: it has no track row, never enters a crate and is not backed
// up (it is rebuilt on request). It is rendered synchronously under the transform runner's slot, so it never runs beside a
// transform. No money and no approval are involved: it is CPU only.

// Limits and defaults of C358.3 and D-DESK-18.
const (
	// AuditionsPerDay is the most new auditions one principal makes per local day (cache hits are free).
	AuditionsPerDay = 200
	// MaxAuditionS is the longest audition: the output including the tail.
	MaxAuditionS = 600
	// AuditionTimeout bounds one render plus its encode.
	AuditionTimeout = 90 * time.Second
	// AuditionWait is how long a request waits for the render slot before AUDITION_BUSY.
	AuditionWait = 30 * time.Second

	auditionTailDefault = 3.0
	auditionGainDefault = 0.6
	auditionMP3         = "192k"
	auditionRetryAfterS = 10
)

// Codes of C350.3 raised by auditions.
const (
	CodeAuditionBusy    = "AUDITION_BUSY"
	CodeAuditionLimit   = "AUDITION_LIMIT"
	CodeAuditionTooLong = "AUDITION_TOO_LONG"
	CodeAuditionFailed  = "AUDITION_FAILED"
)

// Renderer is the part of the transform service an audition uses: the shared score pipeline, the one render slot and the
// configured synth. *transform.Service implements it; tests use a fake.
type Renderer interface {
	RenderScore(ctx context.Context, j transform.ScoreJob, workRel string, progress transform.ChildProgress) (transform.ScoreOut, error)
	Exclusive(ctx context.Context, wait time.Duration, fn func(context.Context) error) error
	ScoreSupport() *transform.ScoreSupport
}

// AuditionIn is the body of an audition request; every field is optional.
type AuditionIn struct {
	Soundfont string   // default: the first configured soundfont
	Chords    string   // none|block, default block (D-DESK-11)
	TailS     *float64 // 0..10, default 3
	Gain      *float64 // 0.1..1.5, default 0.6
}

// Audition is a stored audition (C359.2); URL is signed for the caller.
type Audition struct {
	ID             string  `json:"id"`
	PlanID         string  `json:"plan_id"`
	Soundfont      string  `json:"soundfont"`
	SoundfontSHA12 string  `json:"soundfont_sha256_12"`
	SynthVersion   string  `json:"synth_version"`
	Chords         string  `json:"chords" enum:"none,block"`
	Seconds        float64 `json:"seconds"`
	CPUMS          int64   `json:"cpu_ms"`
	URL            string  `json:"url"`
	CreatedAt      string  `json:"created_at"`
}

// auditionSpec is everything that determines an audition's PCM and its MP3 (C358.3). The field order is the canonical form.
type auditionSpec struct {
	V            int      `json:"v"`
	ScoreSHA256  string   `json:"score_sha256"`
	Dialect      string   `json:"dialect"`
	Chords       string   `json:"chords"`
	BPM          *float64 `json:"bpm"`
	TailS        float64  `json:"tail_s"`
	Gain         float64  `json:"gain"`
	TargetLUFS   float64  `json:"target_lufs"`
	SoundfontSHA string   `json:"soundfont_sha256"`
	SynthVersion string   `json:"synth_version"`
	DSPVersion   string   `json:"dsp_version"`
	MP3          string   `json:"mp3"`
}

func (a auditionSpec) hash() string {
	b, _ := json.Marshal(a) // a struct of strings and numbers cannot fail
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Service) renderer() Renderer {
	if s.o.Transform == nil {
		return nil
	}
	return s.o.Transform
}

func unavailableComponent(c string) error {
	return problem.E(http.StatusServiceUnavailable, "UNAVAILABLE", "auditions are not available: "+c+" is missing", map[string]any{"component": c})
}

// dayWindow is the local day of now: its start (UTC) and the next midnight.
func (s *Service) dayWindow() (start, reset time.Time) {
	z := s.o.Zone
	if z == nil {
		z = time.UTC
	}
	n := s.o.Now().In(z)
	d := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, z)
	return d.UTC(), d.AddDate(0, 0, 1).UTC()
}

const auditionCols = `id, plan_id, soundfont, soundfont_sha256, synth_version, chords, seconds, cpu_ms, mp3_sha256, mp3_path, created_at`

type auditionRow struct {
	Audition
	sha  string
	path string
	sfsh string
}

func scanAudition(sc interface{ Scan(...any) error }) (auditionRow, error) {
	var r auditionRow
	err := sc.Scan(&r.ID, &r.PlanID, &r.Soundfont, &r.sfsh, &r.SynthVersion, &r.Chords, &r.Seconds, &r.CPUMS, &r.sha, &r.path, &r.CreatedAt)
	if len(r.sfsh) >= 12 {
		r.SoundfontSHA12 = r.sfsh[:12]
	}
	return r, err
}

func (s *Service) auditionBySpec(ctx context.Context, q queryer, spec string) (auditionRow, bool, error) {
	r, err := scanAudition(q.QueryRowContext(ctx, `SELECT `+auditionCols+` FROM desk_auditions WHERE spec_hash = ?`, spec))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return r, false, nil
	case err != nil:
		return r, false, err
	}
	return r, true, nil
}

func (s *Service) auditionOut(p auth.Principal, r auditionRow) Audition {
	a := r.Audition
	a.URL = s.auditionURL(p, r.sha)
	return a
}

func (s *Service) fileExists(rel string) bool {
	st, err := s.o.Data.Lstat(rel)
	return err == nil && st.Mode().IsRegular()
}

// resolveAudition validates the request and fills the defaults.
func resolveAudition(in AuditionIn, sup *transform.ScoreSupport) (sf string, chords string, tail, gain float64, err error) {
	sf = in.Soundfont
	if sf == "" {
		sf = sup.Fonts.Names()[0]
	} else if _, _, _, ok := sup.Fonts.Lookup(sf); !ok {
		return "", "", 0, 0, problem.E(http.StatusUnprocessableEntity, "SOUNDFONT_UNKNOWN", "that soundfont is not configured",
			map[string]any{"soundfont": sf, "configured": sup.Fonts.Names()})
	}
	chords = in.Chords
	switch chords {
	case "":
		chords = string(abc.ChordsBlock)
	case string(abc.ChordsNone), string(abc.ChordsBlock):
	default:
		return "", "", 0, 0, invalid("chords", "chords is none or block")
	}
	tail, gain = auditionTailDefault, auditionGainDefault
	if in.TailS != nil {
		if tail = *in.TailS; !(tail >= 0 && tail <= 10) {
			return "", "", 0, 0, invalid("tail_s", "tail_s is 0 to 10 seconds")
		}
	}
	if in.Gain != nil {
		if gain = *in.Gain; !(gain >= 0.1 && gain <= 1.5) {
			return "", "", 0, 0, invalid("gain", "gain is 0.1 to 1.5")
		}
	}
	return sf, chords, tail, gain, nil
}

// Audition renders (or finds) the audition of a plan. created is false for a cache hit, which costs nothing and counts toward no
// limit.
func (s *Service) Audition(ctx context.Context, p auth.Principal, planID string, in AuditionIn) (Audition, bool, error) {
	if err := s.wired(); err != nil {
		return Audition{}, false, err
	}
	r := s.renderer()
	if r == nil {
		return Audition{}, false, unavailableComponent("transform")
	}
	sup := r.ScoreSupport()
	switch {
	case sup == nil || sup.Fonts == nil || len(sup.Fonts.Names()) == 0:
		return Audition{}, false, unavailableComponent("soundfont")
	case sup.Synth == nil:
		return Audition{}, false, unavailableComponent("fluidsynth")
	case s.o.DataDir == "":
		return Audition{}, false, unavailableComponent("transform")
	}
	pl, err := load(ctx, s.o.Store.R, planID)
	if err != nil {
		return Audition{}, false, err
	}
	sf, chords, tail, gain, err := resolveAudition(in, sup)
	if err != nil {
		return Audition{}, false, err
	}
	_, sfSHA, _, _ := sup.Fonts.Lookup(sf)
	spec := auditionSpec{V: 1, ScoreSHA256: pl.scoreSHA, Dialect: pl.dialect, Chords: chords, TailS: tail, Gain: gain,
		TargetLUFS: transform.TargetLUFSDefault, SoundfontSHA: sfSHA, SynthVersion: sup.Info.Version, DSPVersion: transform.DSPVersion, MP3: auditionMP3}
	hash := spec.hash()

	if row, ok, err := s.auditionBySpec(ctx, s.o.Store.R, hash); err != nil {
		return Audition{}, false, err
	} else if ok && s.fileExists(row.path) {
		return s.auditionOut(p, row), false, nil
	}

	text, err := s.readScore(pl.scorePath)
	if err != nil {
		return Audition{}, false, err
	}
	secs, err := auditionSeconds(text, pl.dialect, chords, tail)
	if err != nil {
		return Audition{}, false, err
	}
	if secs > MaxAuditionS {
		return Audition{}, false, problem.E(http.StatusUnprocessableEntity, CodeAuditionTooLong, "the audition would be longer than the limit",
			map[string]any{"seconds": secs, "max": MaxAuditionS})
	}

	var out auditionRow
	var created bool
	run := func(ctx context.Context) error {
		// another request may have made it while this one waited for the slot
		row, ok, err := s.auditionBySpec(ctx, s.o.Store.R, hash)
		if err != nil {
			return err
		}
		if ok && s.fileExists(row.path) {
			out = row
			return nil
		}
		if !ok {
			start, reset := s.dayWindow()
			var n int
			if err := s.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM desk_auditions WHERE principal_id = ? AND created_at >= ?`,
				p.Key(), store.FormatTime(start)).Scan(&n); err != nil {
				return err
			}
			if n >= s.limit() {
				return problem.E(http.StatusTooManyRequests, CodeAuditionLimit, "the daily limit of new auditions is reached",
					map[string]any{"limit": s.limit(), "reset_at": store.FormatTime(reset)})
			}
		}
		rctx, cancel := context.WithTimeout(ctx, AuditionTimeout)
		defer cancel()
		made, err := s.render(rctx, r, text, pl.dialect, chords, tail, gain, sf, sfSHA, hash, sup.Info.Version, p, planID, row, ok)
		if err != nil {
			return err
		}
		out, created = made, !ok
		return nil
	}
	if err := r.Exclusive(ctx, AuditionWait, run); err != nil {
		if errors.Is(err, transform.ErrBusy) {
			return Audition{}, false, problem.E(http.StatusConflict, CodeAuditionBusy, "the renderer is busy, try again shortly",
				map[string]any{"retry_after_s": auditionRetryAfterS})
		}
		return Audition{}, false, err
	}
	return s.auditionOut(p, out), created, nil
}

func (s *Service) limit() int {
	if s.o.AuditionsPerDay > 0 {
		return s.o.AuditionsPerDay
	}
	return AuditionsPerDay
}

// auditionSeconds is the length the audition will have (events plus tail), so an over-long score is refused before any render.
func auditionSeconds(text []byte, dialect, chords string, tail float64) (float64, error) {
	t, err := abc.Parse(text, abc.Options{Dialect: abc.Dialect(dialect)})
	if err != nil {
		return 0, scoreInvalid(err)
	}
	sc, err := t.Score(abc.EventOptions{Chords: abc.ChordMode(chords)})
	if err != nil {
		return 0, scoreInvalid(err)
	}
	if sc.EndTick() <= 0 {
		return 0, scoreInvalid(&abc.SyntaxError{Line: 1, Col: 1, Rule: "empty"})
	}
	return sc.SecondsAt(sc.EndTick()) + tail, nil
}

// render does one pipeline run and, for a new audition, inserts its row. have/old describe an existing row whose file was lost.
func (s *Service) render(ctx context.Context, r Renderer, text []byte, dialect, chords string, tail, gain float64, sf, sfSHA, hash, synthVersion string,
	p auth.Principal, planID string, old auditionRow, have bool) (auditionRow, error) {
	workRel := auditionsRoot + "/work-" + s.o.NewID()
	if err := s.o.Data.MkdirAll(auditionsRoot, 0o755); err != nil {
		return auditionRow{}, fmt.Errorf("desk: %w", unwrapPath(err))
	}
	defer func() { _ = s.o.Data.RemoveAll(workRel) }()
	res, err := r.RenderScore(ctx, transform.ScoreJob{Score: text, Dialect: abc.Dialect(dialect), Chords: abc.ChordMode(chords), TailS: tail, Gain: gain,
		TargetLUFS: transform.TargetLUFSDefault, Soundfont: sf, MaxOutS: MaxAuditionS}, workRel, nil)
	if err != nil {
		return auditionRow{}, auditionErr(ctx, err)
	}
	if res.Seconds > MaxAuditionS {
		return auditionRow{}, problem.E(http.StatusUnprocessableEntity, CodeAuditionTooLong, "the audition would be longer than the limit",
			map[string]any{"seconds": res.Seconds, "max": MaxAuditionS})
	}
	abs := filepath.Join(s.o.DataDir, filepath.FromSlash(workRel))
	mp3 := filepath.Join(abs, "audition.mp3")
	enc := s.o.Encode
	if enc == nil {
		enc = s.ffmpegEncode
	}
	if err := enc(ctx, filepath.Join(abs, "out.pcm24"), mp3); err != nil {
		if ctx.Err() != nil {
			return auditionRow{}, ctx.Err()
		}
		s.o.Log.Warn("desk: audition encode failed", "err", err)
		return auditionRow{}, problem.E(http.StatusInternalServerError, CodeAuditionFailed, "the audition could not be encoded", nil)
	}
	if err := ctx.Err(); err != nil {
		return auditionRow{}, err
	}
	sum, size, err := hashPath(filepath.Join(abs, "audition.mp3"))
	if err != nil {
		return auditionRow{}, fmt.Errorf("desk: %w", unwrapPath(err))
	}
	rel := path.Join(auditionsRoot, hash[:2], hash+".mp3")
	if have {
		rel = old.path
	}
	if err := s.o.Data.MkdirAll(path.Dir(rel), 0o755); err != nil {
		return auditionRow{}, fmt.Errorf("desk: %w", unwrapPath(err))
	}
	if err := s.o.Data.Chmod(path.Join(workRel, "audition.mp3"), 0o444); err != nil {
		return auditionRow{}, fmt.Errorf("desk: %w", unwrapPath(err))
	}
	// the file lands before its row (a file without a row is an orphan the sweep removes; a row without a file is a verify warning)
	if err := s.o.Data.Rename(path.Join(workRel, "audition.mp3"), rel); err != nil {
		return auditionRow{}, fmt.Errorf("desk: %w", unwrapPath(err))
	}
	if have {
		return old, nil // rebuilt in place; the row is immutable
	}
	row := auditionRow{Audition: Audition{ID: s.o.NewID(), PlanID: planID, Soundfont: sf, SoundfontSHA12: sfSHA[:12], SynthVersion: synthVersion, Chords: chords,
		Seconds: res.Seconds, CPUMS: res.CPU.Milliseconds(), CreatedAt: s.now()}, sha: sum, path: rel, sfsh: sfSHA}
	report := res.Report
	if len(report) == 0 || !json.Valid(report) {
		report = json.RawMessage(`{}`)
	}
	_, err = s.o.Store.W.ExecContext(ctx, `INSERT INTO desk_auditions(id, plan_id, spec_hash, soundfont, soundfont_sha256, synth_version, chords,
		mp3_path, mp3_sha256, mp3_bytes, pcm_sha256, seconds, cpu_ms, report_json, principal_id, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.ID, planID, hash, sf, sfSHA, synthVersion, chords, rel, sum, size, res.PCMSHA256, res.Seconds, row.CPUMS, string(report), p.Key(), row.CreatedAt)
	if err != nil {
		// an identical request won, or another setting rendered the very same MP3 bytes: serve that row, and drop the file just placed
		// when it is not the row's own
		prev, ok, qerr := s.auditionBySpec(ctx, s.o.Store.R, hash)
		if qerr == nil && !ok {
			prev, qerr = scanAudition(s.o.Store.R.QueryRowContext(ctx, `SELECT `+auditionCols+` FROM desk_auditions WHERE mp3_sha256 = ?`, sum))
			ok = qerr == nil
		}
		if ok {
			if prev.path != rel {
				_ = s.o.Data.Remove(rel)
			}
			return prev, nil
		}
		return auditionRow{}, err
	}
	audit.NoteCreated(ctx, row.ID)
	return row, nil
}

// auditionErr maps a pipeline failure to the problem model.
func auditionErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var se *transform.ScoreError
	if errors.As(err, &se) {
		switch se.Code {
		case transform.ErrScoreInvalid:
			return problem.E(http.StatusUnprocessableEntity, CodeScoreInvalid, se.Message, map[string]any{"rule": "invalid"})
		case transform.ErrInvalidTransform:
			return problem.E(http.StatusUnprocessableEntity, CodeAuditionTooLong, se.Message, map[string]any{"max": MaxAuditionS})
		case transform.ErrSoundfontUnknown:
			return problem.E(http.StatusUnprocessableEntity, "SOUNDFONT_UNKNOWN", se.Message, nil)
		}
		return problem.E(http.StatusInternalServerError, CodeAuditionFailed, "the audition could not be rendered", map[string]any{"code": se.Code})
	}
	return err
}

func hashPath(p string) (string, int64, error) {
	f, err := os.Open(p) // #nosec G304 -- a path inside the audition work directory this package created
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// ffmpegEncode turns the 24-bit stereo 48 kHz PCM of the pipeline into the audition MP3.
func (s *Service) ffmpegEncode(ctx context.Context, pcm, out string) error {
	if s.o.FFmpeg == "" {
		return errors.New("ffmpeg is not configured")
	}
	_, err := mediatool.Run(ctx, s.o.FFmpeg, []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "s24le", "-ar", "48000", "-ac", "2", "-i", "file:" + pcm,
		"-map_metadata", "-1", "-flags", "+bitexact", "-fflags", "+bitexact",
		"-c:a", "libmp3lame", "-b:a", auditionMP3, "-f", "mp3", "file:" + out},
		mediatool.RunOpts{Timeout: AuditionTimeout, MaxStdout: 1 << 16, Group: true})
	return err
}

// GetAudition returns one audition of a plan.
func (s *Service) GetAudition(ctx context.Context, p auth.Principal, planID, auditionID string) (Audition, error) {
	if err := s.wired(); err != nil {
		return Audition{}, err
	}
	r, err := scanAudition(s.o.Store.R.QueryRowContext(ctx, `SELECT `+auditionCols+` FROM desk_auditions WHERE id = ? AND plan_id = ?`, auditionID, planID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Audition{}, notFound("audition")
	case err != nil:
		return Audition{}, err
	}
	return s.auditionOut(p, r), nil
}

// Resolve serves audition MP3s only (C358.4, the voice-note rule): an address that is not the mp3_sha256 of a row whose file is a
// regular file is not found, so no other derived file is reachable through this resolver. Links are signed for principals who can
// see the plan (Get and the audition operations sign them), never here.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	if s == nil || s.o.Store == nil || s.o.Data == nil || ext != "mp3" || !isKey(sha) {
		return serve.Object{}, serve.ErrNotFound
	}
	var rel string
	switch err := s.o.Store.R.QueryRowContext(ctx, `SELECT mp3_path FROM desk_auditions WHERE mp3_sha256 = ? LIMIT 1`, sha).Scan(&rel); {
	case errors.Is(err, sql.ErrNoRows):
		return serve.Object{}, serve.ErrNotFound
	case err != nil:
		return serve.Object{}, fmt.Errorf("desk: resolve: %w", err)
	}
	st, err := s.o.Data.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return serve.Object{}, serve.ErrNotFound
	case err != nil:
		return serve.Object{}, fmt.Errorf("desk: resolve: %w", unwrapPath(err))
	case !st.Mode().IsRegular():
		return serve.Object{}, serve.ErrNotFound
	}
	return serve.Object{RelPath: rel, ContentType: "audio/mpeg", ETag: sha, ModTime: st.ModTime()}, nil
}

var _ serve.Resolver = (*Service)(nil)

func isKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
