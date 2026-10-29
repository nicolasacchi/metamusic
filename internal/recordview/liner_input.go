package recordview

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/liner"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// day is the YYYY-MM-DD of an RFC 3339 instant.
func day(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// linerDate is the date printed in the notes: the Record's first publication, else its creation (never the clock).
func linerDate(rec records.Record) string {
	if rec.FirstPublishedAt != nil {
		return day(*rec.FirstPublishedAt)
	}
	return day(rec.CreatedAt)
}

// takeCosts reads the display cost of Takes in micro-USD in one statement: the job's settled ledger rows plus its share of the
// batch overhead, else the estimate while the job is not settled (the rule of get_song's cost_display_usd). Takes without a
// job are absent.
func (s *Service) takeCosts(ctx context.Context, ids []string) (map[string]int64, error) {
	out := map[string]int64{}
	if s.o.Store == nil || len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	//nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT t.id, j.expected_micro_usd, j.state, b.n_takes, b.reconciled_at IS NOT NULL,
		(SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE job_id = j.id AND kind = 'settle'),
		(SELECT COUNT(*) FROM ledger WHERE job_id = j.id AND kind = 'settle'),
		(SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = b.id AND kind = 'settle' AND reason = 'overhead')
		FROM tracks t JOIN jobs j ON j.id = t.job_id JOIN batches b ON b.id = j.batch_id WHERE t.id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("recordview: costs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, state string
		var expected, execSettled, n, overhead int64
		var takes int
		var reconciled bool
		if err := rows.Scan(&id, &expected, &state, &takes, &reconciled, &execSettled, &n, &overhead); err != nil {
			return nil, err
		}
		share := int64(0)
		if takes > 0 {
			share = (overhead + int64(takes) - 1) / int64(takes)
		}
		terminal := state == "completed" || state == "failed" || state == "cancelled" || state == "expired"
		if n == 0 && !terminal || n == 0 && state == "completed" && !reconciled {
			out[id] = expected
		} else {
			out[id] = execSettled + share
		}
	}
	return out, rows.Err()
}

// linerInput assembles the liner input for an audience. The app audience reads cost, the first Session's name and the Muse
// line of each Song; the export audience reads none of them unless museNames is on, in which case only the Muse line (the one
// place Muse text can leave the app, D-L2-16).
func (s *Service) linerInput(ctx context.Context, d *data, aud liner.Audience, museNames bool) (liner.Input, error) {
	app := aud == liner.AudienceApp
	museOK := app || museNames
	in := liner.Input{Audience: aud, Title: d.rec.Title, Subtitle: d.rec.Subtitle, Note: d.rec.LinerNote, Date: linerDate(d.rec), MuseNames: museNames}

	var songIDs, museIDs, takeIDs []string
	for _, r := range d.rs {
		songIDs = append(songIDs, r.Slot.SongID)
		if b, ok := d.briefs[r.Slot.SongID]; ok && b.MuseID != nil {
			museIDs = append(museIDs, *b.MuseID)
		}
		if playable(r) {
			takeIDs = append(takeIDs, r.Take.ID)
		}
	}
	songIDs = distinct(songIDs)
	fams, err := s.families(ctx, d, songIDs)
	if err != nil {
		return in, err
	}
	sess, err := s.sessions(ctx, d, songIDs)
	if err != nil {
		return in, err
	}
	muses := map[string]liner.MuseLine{}
	if museOK && s.o.Muses != nil && len(museIDs) > 0 {
		refs, err := s.o.Muses.Refs(ctx, distinct(museIDs))
		if err != nil {
			return in, fmt.Errorf("recordview: muses: %w", err)
		}
		for id, r := range refs {
			muses[id] = liner.MuseLine{Title: r.Title, Artist: r.Artist}
		}
	}
	var costs map[string]int64
	if app {
		if costs, err = s.takeCosts(ctx, distinct(takeIDs)); err != nil {
			return in, err
		}
	}

	for _, r := range d.rs {
		b := d.briefs[r.Slot.SongID]
		t := liner.Track{Position: r.Slot.Position, Title: b.Title, Engine: b.Engine, Families: fams[r.Slot.SongID], Mode: string(r.Mode), BPMBasis: b.BPMBasis}
		if r.Slot.Side != nil {
			t.Side = *r.Slot.Side
		}
		if b.Angle != nil {
			t.Angle = *b.Angle
		}
		if b.BPM != nil {
			n := int(math.Round(*b.BPM))
			t.BPM = &n
		}
		if w := whyOf(d, r.Slot.SongID); w != nil {
			t.Why = *w
		}
		if playable(r) {
			t.DurationS, t.Lossy = r.Take.DurationS, r.Take.Lossy
			if ref, ok := d.refs[r.Take.ID]; ok {
				t.Resampled = ref.SampleRate != tape.Rate
				t.Upmixed = ref.Channels == 1
			}
		}
		if ss := sess[r.Slot.SongID]; len(ss) > 0 {
			if app {
				t.SessionName = ss[0].Name
			} else {
				t.SessionLabel = ss[0].Kind + " " + ss[0].Date
			}
		}
		if app && playable(r) {
			if c, ok := costs[r.Take.ID]; ok {
				t.CostMicroUSD = &c
			}
		}
		if museOK && b.MuseID != nil {
			if m, ok := muses[*b.MuseID]; ok {
				t.Muse = &m
			}
		}
		in.Tracks = append(in.Tracks, t)
	}
	return in, nil
}

// linerView renders the app-audience liner of a Record (Muse text: get_record only).
func (s *Service) linerView(ctx context.Context, d *data) (*LinerView, error) {
	in, err := s.linerInput(ctx, d, liner.AudienceApp, false)
	if err != nil {
		return nil, err
	}
	l := liner.Build(in)
	raw, err := json.Marshal(struct {
		Songs         int      `json:"songs"`
		Playable      int      `json:"playable"`
		Duration      string   `json:"duration"`
		Engines       []string `json:"engines"`
		Angles        []string `json:"angles"`
		Families      []string `json:"families"`
		ContainsLossy bool     `json:"contains_lossy"`
		LossyN        int      `json:"lossy_n"`
	}{l.Summary.Songs, l.Summary.Playable, l.Summary.Duration, nz(l.Summary.Engines), nz(l.Summary.Angles), nz(l.Summary.Families),
		l.Summary.ContainsLossy, l.Summary.LossyN})
	if err != nil {
		return nil, err
	}
	return &LinerView{Markdown: l.Markdown(), Summary: raw}, nil
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
