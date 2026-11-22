package criticbench

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Note is the sentence every answer carries.
const Note = "A critic is trusted only when it agrees with this operator's own ratings; nothing is ever discarded automatically."

// Bench is the answer of get_critic_bench (C387.1).
type Bench struct {
	GeneratedAt time.Time `json:"generated_at"`
	Window      Window    `json:"window"`
	Rule        Rule      `json:"rule"`
	Critics     []Critic  `json:"critics" nullable:"false"`
	Note        string    `json:"note"`
}

// Window sizes what the scores were computed over.
type Window struct {
	RatingPairs int     `json:"rating_pairs"`
	DuelPairs   int     `json:"duel_pairs"`
	Lines       int     `json:"lines"`
	Since       *string `json:"since" doc:"The oldest pair in either window; null when there are none"`
}

// Rule is the trust rule (D-M6N-14).
type Rule struct {
	MinN          int     `json:"min_n"`
	TrustedLower  float64 `json:"trusted_lower"`
	ContraryUpper float64 `json:"contrary_upper"`
}

// Query selects what to return.
type Query struct {
	Critic         string // one critic id; then its disagreements are listed
	IncludeUnknown bool   // false drops the critics that say nothing yet (unknown, insufficient)
}

// Options builds a Service.
type Options struct {
	Store *store.Store
	Now   func() time.Time
}

// Service computes the bench. It only reads.
type Service struct {
	o Options

	mu      sync.Mutex
	sources []Source

	cacheMu  sync.Mutex
	cacheKey string
	cached   *Bench
}

// New builds a Service with the Tier A and agent critics; later lanes add sources with Use.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o, sources: []Source{tierA{}, agents{}}}
}

// Use adds a source of critics (init-time, before the first request).
func (s *Service) Use(src Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sources = append(s.sources, src)
}

func (s *Service) allSources() []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Source(nil), s.sources...)
}

// Bench answers the query from one read snapshot, cached under the data that feeds it (C387.2).
func (s *Service) Bench(ctx context.Context, q Query) (*Bench, error) {
	tx, err := s.o.Store.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("criticbench: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	srcs := s.allSources()
	key, err := s.cacheKeyOf(ctx, tx, srcs)
	if err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	b := s.cached
	hit := b != nil && s.cacheKey == key
	s.cacheMu.Unlock()
	if !hit {
		if b, err = s.compute(ctx, tx, srcs); err != nil {
			return nil, err
		}
		s.cacheMu.Lock()
		s.cached, s.cacheKey = b, key
		s.cacheMu.Unlock()
	}
	return view(b, q)
}

// cacheKeyOf is (count, max(created_at)) of analyses, (count, max(updated_at)) of the Owner's and the critic's ratings, (count, max(rowid)) of duel_verdicts
// and each versioned source's token (C387.2).
func (s *Service) cacheKeyOf(ctx context.Context, tx *sql.Tx, srcs []Source) (string, error) {
	var parts []string
	for _, qy := range []string{
		`SELECT count(*), COALESCE(max(created_at), '') FROM analyses`,
		`SELECT count(*), COALESCE(max(updated_at), '') FROM owner_ratings`, // the ratings table is read through its views (C307.3)
		`SELECT count(*), COALESCE(max(updated_at), '') FROM critic_ratings`,
		`SELECT count(*), COALESCE(max(rowid), 0) FROM duel_verdicts`,
	} {
		var n int
		var m any
		if err := tx.QueryRowContext(ctx, qy).Scan(&n, &m); err != nil {
			return "", fmt.Errorf("criticbench: cache key: %w", err)
		}
		parts = append(parts, strconv.Itoa(n)+"/"+fmt.Sprint(m))
	}
	for _, src := range srcs {
		if v, ok := src.(Versioned); ok {
			tok, err := v.Version(ctx)
			if err != nil {
				return "", fmt.Errorf("criticbench: source version: %w", err)
			}
			parts = append(parts, tok)
		}
	}
	return strings.Join(parts, "|"), nil
}

func (s *Service) compute(ctx context.Context, tx *sql.Tx, srcs []Source) (*Bench, error) {
	ref, err := loadReference(ctx, tx)
	if err != nil {
		return nil, err
	}
	b := &Bench{GeneratedAt: s.o.Now().UTC(), Note: Note, Critics: []Critic{},
		Rule:   Rule{MinN: MinN, TrustedLower: TrustedLower, ContraryUpper: ContraryUpper},
		Window: Window{RatingPairs: len(ref.RatingPairs), DuelPairs: len(ref.DuelPairs)}}
	for _, ps := range [][]Pair{ref.RatingPairs, ref.DuelPairs} {
		for _, p := range ps {
			if b.Window.Since == nil || store.FormatTime(p.At) < *b.Window.Since {
				at := store.FormatTime(p.At)
				b.Window.Since = &at
			}
		}
	}
	for _, src := range srcs {
		cs, err := src.Critics(ctx, tx, ref)
		if err != nil {
			return nil, err
		}
		b.Critics = append(b.Critics, cs...)
		if lc, ok := src.(LineCounter); ok {
			n, err := lc.Lines(ctx)
			if err != nil {
				return nil, fmt.Errorf("criticbench: lines: %w", err)
			}
			b.Window.Lines = max(b.Window.Lines, n)
		}
	}
	sortCritics(b.Critics)
	return b, nil
}

// view applies the query to the cached bench without touching it: it filters, and strips the disagreement lists unless one
// critic was asked for.
func view(b *Bench, q Query) (*Bench, error) {
	out := *b
	out.Critics = make([]Critic, 0, len(b.Critics))
	found := q.Critic == ""
	for _, c := range b.Critics {
		if q.Critic != "" && c.ID != q.Critic {
			continue
		}
		found = true
		if !q.IncludeUnknown && q.Critic == "" && (c.State == StateUnknown || c.State == StateInsufficient) {
			continue
		}
		c.Scores = Scores{Ratings: keep(c.Scores.Ratings, q.Critic != ""), Duels: keep(c.Scores.Duels, q.Critic != ""),
			Lines: keep(c.Scores.Lines, q.Critic != "")}
		out.Critics = append(out.Critics, c)
	}
	if !found {
		return nil, problem.E(404, "NOT_FOUND", "no such critic", map[string]any{"critic": q.Critic})
	}
	return &out, nil
}

func keep(s *Score, disagreements bool) *Score {
	if s == nil {
		return nil
	}
	c := *s
	if !disagreements {
		c.Disagreements = nil
	}
	return &c
}

func loadReference(ctx context.Context, q Reader) (*Reference, error) {
	ref := &Reference{Owner: map[string]int{}}
	roots, at := map[string]string{}, map[string]time.Time{}
	rows, err := q.QueryContext(ctx, `SELECT o.track_id, o.stars, o.updated_at, t.root_track_id FROM owner_ratings o
		JOIN tracks t ON t.id = o.track_id WHERE o.stars IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("criticbench: read owner ratings: %w", err)
	}
	for rows.Next() {
		var id, upd, root string
		var stars int
		if err := rows.Scan(&id, &stars, &upd, &root); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("criticbench: scan owner ratings: %w", err)
		}
		ref.Owner[id], roots[id], at[id] = stars, root, parseAt(upd)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("criticbench: read owner ratings: %w", err)
	}
	ref.RatingPairs = RatingPairs(ref.Owner, roots, at, WindowRatings)

	// the Owner's blind verdicts: duels are Owner-only to create and judge, so the human verdict is the Owner's
	rows, err = q.QueryContext(ctx, `SELECT d.id, d.slot_a_track_id, d.slot_b_track_id, v.winner_track_id, v.created_at
		FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id
		WHERE v.source = 'human' AND d.blind = 1 AND v.saw_reveal = 0 AND v.verdict IN ('a','b')
		ORDER BY v.created_at DESC, v.id LIMIT ?`, WindowDuels)
	if err != nil {
		return nil, fmt.Errorf("criticbench: read duel verdicts: %w", err)
	}
	for rows.Next() {
		var id, a, b, w, created string
		if err := rows.Scan(&id, &a, &b, &w, &created); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("criticbench: scan duel verdicts: %w", err)
		}
		ref.DuelPairs = append(ref.DuelPairs, Pair{ID: id, A: a, B: b, Winner: w, At: parseAt(created)})
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("criticbench: read duel verdicts: %w", err)
	}
	ref.DuelPairs = Newest(ref.DuelPairs, WindowDuels)
	return ref, nil
}

func closeRows(r *sql.Rows) error {
	err := r.Err()
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	return err
}

func parseAt(s string) time.Time {
	for _, layout := range []string{store.TimeLayout, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
