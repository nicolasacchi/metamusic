package songview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func notFound(id string, pending bool) error {
	d := map[string]any{"id": id}
	if pending {
		d["pending"] = true
	}
	return problem.E(http.StatusNotFound, "NOT_FOUND", "no such song: "+id, d)
}

// resolve maps the id of a Get call to a Song id (C90.6): a Song id as is, a draft id to its live Song (else the
// Song that owns its fulfilling root), a take id through the take mapping. An unknown id is 404; details.pending
// says the draft or take exists but its provenance key is still queued.
func (s *Service) resolve(ctx context.Context, by, id string) (string, error) {
	var out sql.NullString
	switch by {
	case "", "song":
		return id, nil
	case "draft":
		err := s.st.R.QueryRowContext(ctx, `SELECT COALESCE((SELECT id FROM songs WHERE draft_id = ? AND retired_at IS NULL),
			(SELECT r.song_id FROM recipe_drafts d JOIN tracks t ON t.id = d.fulfilled_by_track_id JOIN song_roots r ON r.root_track_id = t.root_track_id WHERE d.id = ?))`, id, id).Scan(&out)
		if err != nil {
			return "", err
		}
		if out.Valid {
			return out.String, nil
		}
		return "", notFound(id, s.keyQueued(ctx, "recipe_drafts", "draft", id))
	case "track":
		err := s.st.R.QueryRowContext(ctx, `SELECT song_id FROM take_songs WHERE track_id = ?`, id).Scan(&out)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if out.Valid {
			return out.String, nil
		}
		return "", notFound(id, s.keyQueued(ctx, "tracks", "track", id))
	}
	return "", problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "by must be song, draft or track", map[string]any{"field": "by"})
}

// keyQueued reports whether the entity exists and its provenance key is queued (the answer is "retry after a pass").
func (s *Service) keyQueued(ctx context.Context, table, kind, id string) bool {
	var n int
	q := `SELECT EXISTS(SELECT 1 FROM ` + table + ` WHERE id = ?) AND EXISTS(SELECT 1 FROM provenance_dirty WHERE kind = ? AND id = ?)`
	if err := s.st.R.QueryRowContext(ctx, q, id, kind, id).Scan(&n); err != nil {
		return false
	}
	return n == 1
}

var bpmRE = regexp.MustCompile(`(?i)(\d{2,3})\s*bpm`)

// usd renders non-negative micro-USD as the exact decimal dollar string money.MicroUSD.USD gives (songview does not
// import money, C87).
func usd(m int64) string {
	whole, frac := m/1_000_000, m%1_000_000
	if frac == 0 {
		return strconv.FormatInt(whole, 10)
	}
	f := strings.TrimRight(strconv.FormatInt(frac+1_000_000, 10)[1:], "0")
	return strconv.FormatInt(whole, 10) + "." + f
}

// jobCostUSD is library.JobCost's display rule (reconciled, else provisional, else estimate) as a dollar string.
func (s *Service) jobCostUSD(ctx context.Context, jobID string) (*string, error) {
	var expected, execSettled, rows, overhead int64
	var state string
	var takes int
	var reconciled bool
	err := s.st.R.QueryRowContext(ctx, `SELECT j.expected_micro_usd, j.state, b.n_takes, b.reconciled_at IS NOT NULL,
		(SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE job_id = j.id AND kind = 'settle'),
		(SELECT COUNT(*) FROM ledger WHERE job_id = j.id AND kind = 'settle'),
		(SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = b.id AND kind = 'settle' AND reason = 'overhead')
		FROM jobs j JOIN batches b ON b.id = j.batch_id WHERE j.id = ?`, jobID).Scan(&expected, &state, &takes, &reconciled, &execSettled, &rows, &overhead)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	share := int64(0)
	if takes > 0 {
		share = (overhead + int64(takes) - 1) / int64(takes)
	}
	terminal := state == "completed" || state == "failed" || state == "cancelled" || state == "expired"
	var v int64
	switch {
	case rows == 0 && !terminal, rows == 0 && state == "completed" && !reconciled:
		v = expected
	default:
		v = execSettled + share
	}
	out := usd(v)
	return &out, nil
}

type takeRow struct {
	card      TakeCard
	recorded  int64
	declared  sql.NullInt64
	style     string
	jobID     sql.NullString
	measured  sql.NullFloat64
	recAtText string
}

// Get assembles the view of one Song (C90.6).
func (s *Service) Get(ctx context.Context, p auth.Principal, id string, o GetOptions) (View, error) {
	takeLimit := o.TakeLimit
	if takeLimit <= 0 {
		takeLimit = 24
	}
	takeLimit = min(takeLimit, 100)
	events := o.IncludeEvents
	if events < 0 {
		events = 10
	}
	events = min(events, 50)

	songID, err := s.resolve(ctx, o.By, id)
	if err != nil {
		return View{}, err
	}
	row, err := s.songs.Get(ctx, songID)
	if err != nil {
		var pe *problem.Error
		if errors.As(err, &pe) && pe.Code == "NOT_FOUND" {
			return View{}, notFound(id, false)
		}
		return View{}, err
	}
	v := View{Song: SongCore{
		ID: row.ID, Title: row.Title, Angle: row.Angle, AngleSource: row.AngleSource, Origin: row.Origin, Revision: row.Revision,
		Hidden: row.HiddenAt != nil, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, RootTrackID: row.RootTrackID, DraftID: row.DraftID,
		Retired: row.RetiredAt != nil, RetiredInto: row.RetiredInto, RootTrackIDs: []string{},
	}, Takes: []TakeCard{}, Proposals: []picks.Proposal{}, Events: []picks.PickEvent{}, Sessions: []SessionRef{},
		Sounds: SoundsBlock{Facets: map[string][]FacetEntry{}, Unmapped: []string{}}}
	if err := s.state(ctx, &v.Song); err != nil {
		return View{}, err
	}
	if row.MuseID != nil {
		if r, ok, err := s.muses.Ref(ctx, *row.MuseID); err != nil {
			return View{}, err
		} else if ok {
			v.Muse = &r
		}
	}
	if row.RetiredAt != nil {
		return v, nil
	}
	if err := s.fill(ctx, &v, row, takeLimit, events); err != nil {
		return View{}, err
	}
	return v, nil
}

// state fills state, archived and queued_jobs of the core from the shared state SQL (C90.4).
func (s *Service) state(ctx context.Context, c *SongCore) error {
	var st string
	var archived bool
	err := s.st.R.QueryRowContext(ctx, `SELECT `+store.SongStateCase+`, st.queued_jobs, st.archived FROM (`+store.SongStateSQL+` WHERE s.id = ?) st`, c.ID).Scan(&st, &c.QueuedJobs, &archived)
	if err != nil {
		return fmt.Errorf("songview: state: %w", err)
	}
	c.State, c.Archived = songs.State(st), archived
	return nil
}

func (s *Service) fill(ctx context.Context, v *View, row songs.Song, takeLimit, events int) error {
	r := s.st.R
	// roots, primary first
	rows, err := r.QueryContext(ctx, `SELECT root_track_id FROM song_roots WHERE song_id = ? ORDER BY root_track_id = ? DESC, root_track_id`, row.ID, row.RootTrackID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		v.Song.RootTrackIDs = append(v.Song.RootTrackIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if row.DraftID != nil {
		var d DraftBlock
		var label, group sql.NullString
		err := r.QueryRowContext(ctx, `SELECT id, status, revision, label, request_id, group_key FROM recipe_drafts WHERE id = ?`, *row.DraftID).Scan(&d.ID, &d.Status, &d.Revision, &label, &d.RequestID, &group)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			d.Label = label.String
			if group.Valid {
				d.GroupKey = &group.String
			}
			v.Draft = &d
		}
	}
	// pick
	pick, err := s.pick(ctx, row.ID)
	if err != nil {
		return err
	}
	v.Pick = pick
	// takes
	trs, err := s.takeRows(ctx, row.ID)
	if err != nil {
		return err
	}
	v.TakesTotal = len(trs)
	slices.SortFunc(trs, func(a, b takeRow) int {
		ap := pick != nil && pick.TakeID != nil && *pick.TakeID == a.card.ID
		bp := pick != nil && pick.TakeID != nil && *pick.TakeID == b.card.ID
		switch {
		case ap != bp && ap:
			return -1
		case ap != bp:
			return 1
		case a.recorded != b.recorded:
			return int(b.recorded - a.recorded)
		}
		return strings.Compare(b.card.ID, a.card.ID)
	})
	if len(trs) > takeLimit {
		trs = trs[:takeLimit]
	}
	ids := make([]string, len(trs))
	for i, t := range trs {
		ids[i] = t.card.ID
	}
	var strengths map[string]ladder.Strength
	if s.ladder != nil && len(ids) > 0 {
		if strengths, _, err = s.ladder.Strengths(ctx, ids); err != nil {
			return fmt.Errorf("songview: ladder: %w", err)
		}
	}
	var flags map[string][]string
	if s.flags != nil && len(ids) > 0 {
		if flags, err = s.flags.FlagCodes(ctx, ids); err != nil {
			return fmt.Errorf("songview: flags: %w", err)
		}
	}
	for _, t := range trs {
		c := t.card
		c.IsPick = pick != nil && pick.TakeID != nil && *pick.TakeID == c.ID
		c.Pinned = pick != nil && pick.PinnedTakeID != nil && *pick.PinnedTakeID == c.ID
		c.Flags = []string{}
		if f := flags[c.ID]; len(f) > 0 {
			c.Flags = f
		}
		if st, ok := strengths[c.ID]; ok {
			st := st
			c.Ladder = &st
		}
		var req *float64
		switch {
		case t.declared.Valid:
			f := float64(t.declared.Int64)
			req = &f
		default:
			if m := bpmRE.FindStringSubmatch(t.style); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					f := float64(n)
					req = &f
				}
			}
		}
		var meas *float64
		if t.measured.Valid {
			f := t.measured.Float64
			meas = &f
		}
		c.RequestedBPM, c.MeasuredBPM = req, meas
		if t.jobID.Valid {
			if c.CostDisplayUSD, err = s.jobCostUSD(ctx, t.jobID.String); err != nil {
				return err
			}
		}
		v.Takes = append(v.Takes, c)
	}
	// proposals (open) and events (newest first)
	if v.Proposals, err = s.openProposals(ctx, row.ID); err != nil {
		return err
	}
	if v.Events, err = s.recentEvents(ctx, row.ID, events); err != nil {
		return err
	}
	if err := s.soundsBlock(ctx, v, row.ID); err != nil {
		return err
	}
	if err := s.sessionRefs(ctx, v, row); err != nil {
		return err
	}
	if len(v.Song.RootTrackIDs) > 0 && row.RootTrackID != nil {
		var l LineageBlock
		l.RootTrackID = *row.RootTrackID
		var maxGen sql.NullInt64
		if err := r.QueryRowContext(ctx, `SELECT MAX(generation), SUM(generation > 0) FROM tracks WHERE root_track_id = ?`, l.RootTrackID).Scan(&maxGen, &l.ChildrenTotal); err != nil {
			return err
		}
		l.Generations = int(maxGen.Int64) + 1
		v.Lineage = &l
	}
	var pending int
	if err := r.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM provenance_dirty d WHERE
		(d.kind = 'track' AND d.id IN (SELECT track_id FROM take_songs WHERE song_id = ?)) OR (d.kind = 'draft' AND d.id = ?))`, row.ID, nullStr(row.DraftID)).Scan(&pending); err != nil {
		return err
	}
	v.Pending = pending == 1
	if v.Pick != nil {
		v.Pick.Pending = v.Pending
	}
	return nil
}

func nullStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func (s *Service) takeRows(ctx context.Context, songID string) ([]takeRow, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT t.id, t.title, t.status, t.generation, t.parent_track_id, t.origin, t.duration_s, t.recorded_at, t.lossy,
		(r.engine <> 'unknown'), hs.stars, ha.stars, t.bpm_declared, r.style, t.job_id,
		EXISTS(SELECT 1 FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'loudness'),
		(SELECT json_extract(a.metrics_json, '$.bpm') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'tempo' ORDER BY a.created_at DESC, a.version DESC LIMIT 1)
		FROM take_songs ts JOIN tracks t ON t.id = ts.track_id JOIN recipes r ON r.id = t.recipe_id
		LEFT JOIN ratings hs ON hs.track_id = t.id AND hs.source = 'human' LEFT JOIN ratings ha ON ha.track_id = t.id AND ha.source = 'agent'
		WHERE ts.song_id = ?`, songID)
	if err != nil {
		return nil, fmt.Errorf("songview: takes: %w", err)
	}
	defer rows.Close()
	var out []takeRow
	for rows.Next() {
		var t takeRow
		var parent, jobID sql.NullString
		var stars, critic sql.NullInt64
		var lossy, recipeKnown, ears bool
		if err := rows.Scan(&t.card.ID, &t.card.Title, &t.card.Status, &t.card.Generation, &parent, &t.card.Origin, &t.card.DurationS, &t.recAtText, &lossy,
			&recipeKnown, &stars, &critic, &t.declared, &t.style, &jobID, &ears, &t.measured); err != nil {
			return nil, err
		}
		t.card.RecordedAt = t.recAtText
		if parent.Valid {
			t.card.ParentTrackID = &parent.String
		}
		t.card.Lossy, t.card.RecipeKnown, t.card.HasEars = lossy, recipeKnown, ears
		if stars.Valid {
			v := int(stars.Int64)
			t.card.Stars = &v
		}
		if critic.Valid {
			v := int(critic.Int64)
			t.card.CriticStars = &v
		}
		t.jobID = jobID
		if tm, err := store.ParseInstant(t.recAtText); err == nil {
			t.recorded = tm.UnixMilli()
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// pick reads song_picks into a picks.PickView (C91.4); nil when the Song never had one. songview reads the row
// itself so get_song works while picks.Service is a different lane's concern.
func (s *Service) pick(ctx context.Context, songID string) (*picks.PickView, error) {
	var pv picks.PickView
	var take, pinned, pinnedBy, pinnedAt sql.NullString
	var evidence string
	var seq sql.NullInt64
	err := s.st.R.QueryRowContext(ctx, `SELECT p.take_id, p.source, p.pinned_take_id, p.pinned_by, p.pinned_at, p.evidence_json, p.changed_at, p.last_event_id,
		(SELECT e.seq FROM pick_events e WHERE e.id = p.last_event_id),
		(SELECT COUNT(*) FROM pick_proposals q WHERE q.song_id = p.song_id AND q.state = 'open')
		FROM song_picks p WHERE p.song_id = ?`, songID).Scan(&take, &pv.Source, &pinned, &pinnedBy, &pinnedAt, &evidence, &pv.ChangedAt, &pv.LastEventID, &seq, &pv.OpenProposals)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("songview: pick: %w", err)
	}
	if take.Valid {
		pv.TakeID = &take.String
	}
	if pinned.Valid {
		pv.PinnedTakeID = &pinned.String
		pv.PinIneligible = pv.Source != picks.SourcePinned
	}
	if pinnedBy.Valid {
		pv.PinnedBy = &pinnedBy.String
		kind, _, _ := strings.Cut(pinnedBy.String, ":")
		pv.PinnedByKind = &kind
		name := pinnedBy.String
		_ = s.st.R.QueryRowContext(ctx, `SELECT actor_name FROM pick_events WHERE song_id = ? AND actor = ? ORDER BY seq DESC LIMIT 1`, songID, pinnedBy.String).Scan(&name)
		pv.PinnedByName = &name
	}
	if pinnedAt.Valid {
		pv.PinnedAt = &pinnedAt.String
	}
	pv.Evidence = json.RawMessage(evidence)
	pv.Seq = int(seq.Int64)
	return &pv, nil
}

func (s *Service) openProposals(ctx context.Context, songID string) ([]picks.Proposal, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT id, song_id, take_id, rationale, principal_id, principal_name, state, created_at, decided_at, decided_by
		FROM pick_proposals WHERE song_id = ? AND state = 'open' ORDER BY created_at, id`, songID)
	if err != nil {
		return nil, fmt.Errorf("songview: proposals: %w", err)
	}
	defer rows.Close()
	out := []picks.Proposal{}
	for rows.Next() {
		var p picks.Proposal
		var at, by sql.NullString
		if err := rows.Scan(&p.ID, &p.SongID, &p.TakeID, &p.Rationale, &p.PrincipalID, &p.PrincipalName, &p.State, &p.CreatedAt, &at, &by); err != nil {
			return nil, err
		}
		if at.Valid {
			p.DecidedAt = &at.String
		}
		if by.Valid {
			p.DecidedBy = &by.String
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) recentEvents(ctx context.Context, songID string, n int) ([]picks.PickEvent, error) {
	out := []picks.PickEvent{}
	if n == 0 {
		return out, nil
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT rowid, id, song_id, seq, at, from_take_id, to_take_id, from_source, to_source, reason, actor, actor_kind, actor_name, evidence_json
		FROM pick_events WHERE song_id = ? ORDER BY seq DESC LIMIT ?`, songID, n)
	if err != nil {
		return nil, fmt.Errorf("songview: events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e picks.PickEvent
		var from, to sql.NullString
		var ev string
		if err := rows.Scan(&e.Cursor, &e.ID, &e.SongID, &e.Seq, &e.At, &from, &to, &e.FromSource, &e.ToSource, &e.Reason, &e.Actor, &e.ActorKind, &e.ActorName, &ev); err != nil {
			return nil, err
		}
		if from.Valid {
			e.FromTakeID = &from.String
		}
		if to.Valid {
			e.ToTakeID = &to.String
		}
		e.Evidence = json.RawMessage(ev)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) soundsBlock(ctx context.Context, v *View, songID string) error {
	r := s.st.R
	if err := r.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM sound_taxonomy_versions`).Scan(&v.Sounds.TaxonomyVersion); err != nil {
		return err
	}
	rows, err := r.QueryContext(ctx, `SELECT facet, value, origin, detail_json FROM song_sounds WHERE song_id = ? ORDER BY facet, value`, songID)
	if err != nil {
		return fmt.Errorf("songview: sounds: %w", err)
	}
	for rows.Next() {
		var facet, value, origin string
		var detail sql.NullString
		if err := rows.Scan(&facet, &value, &origin, &detail); err != nil {
			_ = rows.Close()
			return err
		}
		v.Sounds.Facets[facet] = append(v.Sounds.Facets[facet], FacetEntry{Value: value, Origin: origin})
		if facet == "tempo_band" && detail.Valid && v.Sounds.Tempo == nil {
			var td sounds.TempoDetail
			if json.Unmarshal([]byte(detail.String), &td) == nil && td.Basis != "" {
				v.Sounds.Tempo = &td
			}
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = r.QueryContext(ctx, `SELECT term FROM song_unmapped_terms WHERE song_id = ? ORDER BY term LIMIT 10`, songID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return err
		}
		v.Sounds.Unmapped = append(v.Sounds.Unmapped, t)
	}
	return rows.Err()
}

func (s *Service) sessionRefs(ctx context.Context, v *View, row songs.Song) error {
	rows, err := s.st.R.QueryContext(ctx, `SELECT x.id, x.kind, x.name FROM sessions x WHERE x.hidden_at IS NULL AND x.id IN (
		SELECT m.session_id FROM session_members m WHERE m.member_kind = 'track' AND m.member_id IN (SELECT track_id FROM take_songs WHERE song_id = ?)
		UNION SELECT m.session_id FROM session_members m WHERE m.member_kind = 'draft' AND m.member_id = ?)
		ORDER BY x.started_at DESC, x.id DESC LIMIT 10`, row.ID, nullStr(row.DraftID))
	if err != nil {
		return fmt.Errorf("songview: sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r SessionRef
		if err := rows.Scan(&r.ID, &r.Kind, &r.Name); err != nil {
			return err
		}
		v.Sessions = append(v.Sessions, r)
	}
	return rows.Err()
}
