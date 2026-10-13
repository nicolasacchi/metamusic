package duels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// duelRow is a stored duel.
type duelRow struct {
	ID, SlotA, SlotB    string
	Swapped, Blind      bool
	FromMS, ToMS        int64
	ClipDef, Tool       string
	Match               string
	TargetLUFS          sql.NullFloat64
	GainACDB, GainBCDB  int
	LUFSA, LUFSB        sql.NullFloat64
	TPA, TPB            sql.NullFloat64
	CreatedBy, Kind     string
	CreatedAt, ExpireAt time.Time
}

const duelCols = `d.id, d.slot_a_track_id, d.slot_b_track_id, d.swapped, d.blind, d.from_ms, d.to_ms, d.clip_def, d.tool_version, d.match,
	d.target_lufs, d.gain_a_cdb, d.gain_b_cdb, d.window_lufs_a, d.window_lufs_b, d.window_tp_a, d.window_tp_b,
	d.created_by, d.created_by_kind, d.created_at, d.expires_at`

type scanner interface{ Scan(...any) error }

func scanDuel(sc scanner) (duelRow, error) {
	var d duelRow
	var created, expires string
	if err := sc.Scan(&d.ID, &d.SlotA, &d.SlotB, &d.Swapped, &d.Blind, &d.FromMS, &d.ToMS, &d.ClipDef, &d.Tool, &d.Match,
		&d.TargetLUFS, &d.GainACDB, &d.GainBCDB, &d.LUFSA, &d.LUFSB, &d.TPA, &d.TPB, &d.CreatedBy, &d.Kind, &created, &expires); err != nil {
		return d, err
	}
	var err error
	if d.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return d, fmt.Errorf("duels: created_at of %s: %w", d.ID, err)
	}
	if d.ExpireAt, err = time.Parse(time.RFC3339, expires); err != nil {
		return d, fmt.Errorf("duels: expires_at of %s: %w", d.ID, err)
	}
	return d, nil
}

// verdictRow is a stored verdict.
type verdictRow struct {
	ID, DuelID, Source, Kind, Principal, Verdict string
	Winner, Note                                 sql.NullString
	Latency, Switches                            sql.NullInt64
	SawReveal                                    bool
	CreatedAt                                    string
}

const verdictCols = `v.id, v.duel_id, v.source, v.principal_kind, v.principal_id, v.verdict, v.winner_track_id, v.note, v.latency_ms, v.switches, v.saw_reveal, v.created_at`

func scanVerdict(sc scanner) (verdictRow, error) {
	var v verdictRow
	err := sc.Scan(&v.ID, &v.DuelID, &v.Source, &v.Kind, &v.Principal, &v.Verdict, &v.Winner, &v.Note, &v.Latency, &v.Switches, &v.SawReveal, &v.CreatedAt)
	return v, err
}

// loadDuel reads one duel; ErrNotFound-style problem when absent.
func (s *Service) loadDuel(ctx context.Context, id string) (duelRow, error) {
	d, err := scanDuel(s.o.Store.R.QueryRowContext(ctx, `SELECT `+duelCols+` FROM duels d WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, notFound("duel", id)
	}
	if err != nil {
		return d, fmt.Errorf("duels: load: %w", err)
	}
	return d, nil
}

// loadVerdicts returns the verdicts of the given duels, oldest first, keyed by duel.
func (s *Service) loadVerdicts(ctx context.Context, ids ...string) (map[string][]verdictRow, error) {
	out := make(map[string][]verdictRow, len(ids))
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		//nolint:gosec // G202: only "?" placeholders and a constant column list are concatenated
		rows, err := s.o.Store.R.QueryContext(ctx, `SELECT `+verdictCols+` FROM duel_verdicts v WHERE v.duel_id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")+`) ORDER BY v.created_at, v.id`, args...)
		if err != nil {
			return nil, fmt.Errorf("duels: verdicts: %w", err)
		}
		for rows.Next() {
			v, err := scanVerdict(rows)
			if err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("duels: verdicts: %w", err)
			}
			out[v.DuelID] = append(out[v.DuelID], v)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// standing is everything the visibility rules need to know about one caller and one duel.
type standing struct {
	human    *verdictRow  // the human-source verdict, whoever cast it
	mine     *verdictRow  // the caller's own critic verdict (critic callers only)
	critics  []verdictRow // every critic verdict
	source   string       // "human", "critic" or "" (system, anonymous)
	revealed bool
	judged   bool // the caller has judged this duel (C48.3)
	state    string
}

func (s *Service) standingOf(p auth.Principal, d duelRow, vs []verdictRow) standing {
	st := standing{}
	st.source, _ = SourceFor(p)
	for i := range vs {
		v := &vs[i]
		switch v.Source {
		case "human":
			st.human = v
		case "critic":
			st.critics = append(st.critics, *v)
			if st.source == "critic" && v.Principal == p.Key() {
				st.mine = v
			}
		}
	}
	st.revealed = !d.Blind || st.human != nil || st.mine != nil
	st.judged = (st.source == "human" && st.human != nil) || (st.source == "critic" && st.mine != nil)
	switch {
	case st.human != nil:
		st.state = "decided"
	case !s.now().Before(d.ExpireAt):
		st.state = "expired"
	default:
		st.state = "open"
	}
	return st
}

// mineVerdict is the verdict shown as the caller's own: the human verdict for a
// human-source caller (whoever cast it, D-M2b-18), the caller's critic verdict otherwise.
func (st standing) mineVerdict() *verdictRow {
	switch st.source {
	case "human":
		return st.human
	case "critic":
		return st.mine
	}
	return nil
}

func (d duelRow) window() Window {
	return Window{FromS: float64(d.FromMS) / 1000, ToS: float64(d.ToMS) / 1000, DurationS: float64(d.ToMS-d.FromMS) / 1000}
}

func (d duelRow) loudnessMatched() bool { return d.Match == "matched" || d.Match == "limited" }

func brief(r library.TrackRef) *TrackBriefOrNull {
	b := TrackBriefOrNull{ID: r.ID, Title: r.Title, Seed: r.Seed, RequestID: r.RequestID, RootTrackID: r.RootTrackID, DurationS: r.DurationS}
	return &b
}

func nullFloat(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func nullStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

func verdictView(v verdictRow, revealed bool) Verdict {
	out := Verdict{ID: v.ID, Source: v.Source, PrincipalKind: v.Kind, Verdict: v.Verdict, Note: nullStr(v.Note),
		SawReveal: v.SawReveal, CreatedAt: v.CreatedAt}
	if revealed {
		out.WinnerTrackID = nullStr(v.Winner)
	}
	if v.Latency.Valid {
		l := v.Latency.Int64
		out.LatencyMS = &l
	}
	if v.Switches.Valid {
		sw := int(v.Switches.Int64)
		out.Switches = &sw
	}
	return out
}

// spec builds the clip spec of one slot.
func (d duelRow) spec(r library.TrackRef, slotB bool) clip.Spec {
	g := d.GainACDB
	if slotB {
		g = d.GainBCDB
	}
	return clip.Spec{MasterSHA256: r.MasterSHA256, MasterPath: r.MasterPath, SampleRate: r.SampleRate, Channels: r.Channels,
		W: clip.Window{StartMS: d.FromMS, EndMS: d.ToMS}, GainCDB: g}
}

// clipRef signs a link to a rendered clip for p.
func (s *Service) clipRef(p auth.Principal, o clip.Object) ClipRef {
	ttl, bucket := serve.TTLFor(p)
	l := s.o.Signer.Sign(o.Key, "flac", ttl, bucket)
	return ClipRef{URL: l.URL, ExpiresAt: rfc(l.Expires), TTLS: int64(ttl / time.Second), ContentType: "audio/flac", Bytes: o.Bytes, DurationS: o.DurationS}
}

// refs loads the two slot tracks of d.
func (s *Service) refs(ctx context.Context, d duelRow) (a, b library.TrackRef, err error) {
	m, err := s.o.Library.TrackRefs(ctx, []string{d.SlotA, d.SlotB})
	if err != nil {
		return a, b, err
	}
	a, okA := m[d.SlotA]
	b, okB := m[d.SlotB]
	if !okA || !okB {
		return a, b, fmt.Errorf("duels: duel %s references a missing track", d.ID)
	}
	return a, b, nil
}

// view renders d for p. objs holds already ensured clips (Create); otherwise the clips are
// ensured here (a cache fill). With bestEffort, a clip that cannot be produced is left empty
// instead of failing the call (submit: the verdict is already stored).
func (s *Service) view(ctx context.Context, p auth.Principal, d duelRow, vs []verdictRow, objs *[2]clip.Object, bestEffort bool) (Duel, error) {
	st := s.standingOf(p, d, vs)
	ra, rb, err := s.refs(ctx, d)
	if err != nil {
		return Duel{}, err
	}
	out := Duel{ID: d.ID, State: st.state, Blind: d.Blind, Revealed: st.revealed, CreatedAt: rfc(d.CreatedAt), ExpiresAt: rfc(d.ExpireAt),
		CreatedByKind: d.Kind, Window: d.window(), LoudnessMatched: d.loudnessMatched(), CriticVerdicts: []Verdict{}}
	var clips [2]ClipRef
	for i, r := range [2]library.TrackRef{ra, rb} {
		var obj clip.Object
		switch {
		case objs != nil:
			obj = objs[i]
		case s.o.Clips == nil && bestEffort:
			continue
		default:
			obj, err = s.o.Clips.Ensure(ctx, d.spec(r, i == 1))
			if err != nil {
				if bestEffort {
					continue
				}
				return Duel{}, err
			}
		}
		clips[i] = s.clipRef(p, obj)
	}
	out.Slots = Slots{A: Slot{Clip: clips[0]}, B: Slot{Clip: clips[1]}}
	if st.revealed {
		out.Slots.A.Track, out.Slots.B.Track = brief(ra), brief(rb)
		g := func(c int) *float64 { v := float64(c) / 100; return &v }
		out.Slots.A.GainDB, out.Slots.B.GainDB = g(d.GainACDB), g(d.GainBCDB)
		out.Slots.A.WindowLUFS, out.Slots.B.WindowLUFS = nullFloat(d.LUFSA), nullFloat(d.LUFSB)
	}
	if mine := st.mineVerdict(); mine != nil {
		v := VerdictOrNull(verdictView(*mine, st.revealed))
		out.MyVerdict = &v
	}
	if st.revealed && st.human != nil {
		v := VerdictOrNull(verdictView(*st.human, true))
		out.HumanVerdict = &v
	}
	if st.judged {
		for _, c := range st.critics {
			out.CriticVerdicts = append(out.CriticVerdicts, verdictView(c, true))
		}
	}
	return out, nil
}

// Get returns p's view of a duel with fresh clip links.
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Duel, error) {
	if s.unconfigured() {
		return Duel{}, errors.ErrUnsupported
	}
	if s.o.Clips == nil {
		return Duel{}, unavailable("clips")
	}
	d, err := s.loadDuel(ctx, id)
	if err != nil {
		return Duel{}, err
	}
	vs, err := s.loadVerdicts(ctx, id)
	if err != nil {
		return Duel{}, err
	}
	return s.view(ctx, p, d, vs[id], nil, false)
}

func round4(x float64) float64 { return math.Round(x*1e4) / 1e4 }
