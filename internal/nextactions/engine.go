package nextactions

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Evidence conventions. A rule's Evidence holds numbers and ids only. Keys that start with "_" are private to the rule: Text and
// Hint read them, the engine removes them before anything leaves the package. "_total" is the number of candidates before the
// rule's own cut (a COUNT(*) OVER () column), so the per-code cap can say how many more exist without fetching them.
const (
	evTotal = "_total"

	// scanMax bounds the candidates a rule returns when the call asks about one subject (a filter or a take): the subject is then
	// picked out in memory, so the per-rule limit of 4*PerCode must not hide it.
	scanMax = 2000
)

// SubjectTake is the subject kind of the take rules of M5-C1 (C257): the subject id is a take (track) id.
const SubjectTake = "take"

// takeCodes are the rules whose subject is a take (rules_d.go). ForTake evaluates them for the take itself, also when the take has
// no Song.
var takeCodes = map[string]bool{"callback_waiting": true, "regressed_comment": true, "open_fix_unaddressed": true, "patch_ready_to_render": true}

// forTakeCodes are the rules ForTake evaluates: the ones whose subject is a take's Song, its Muse or a Record holding it (C139.4).
// Codes of rules that NA2 and NA3 add are listed now, so the set does not move when they land.
var forTakeCodes = map[string]bool{
	"song_duel": true, "song_proposals_waiting": true, "song_tempo_mismatch": true, "song_no_muse": true,
	"muse_partial_angles": true, "muse_unjudged": true, "muse_record_candidate": true, "record_undecided_slots": true, "record_empty_slot": true,
	"callback_waiting": true, "regressed_comment": true, "open_fix_unaddressed": true, "patch_ready_to_render": true,
}

// txReader is the Reader every rule sees: one read-only transaction, plus the live-Muse parameter and the statement hook of the
// tests.
type txReader struct {
	tx   *sql.Tx
	live string // JSON array of the live Muse ids the rules of this call may use
	// songs is a JSON array of the only Song ids the call is about (a take's Song, a filter on one Song), or "" for every Song. A
	// rule about Songs restricts its statement through SongScope; a rule about Muses or Records ignores it.
	songs string
	// takes is the same for the take rules of M5-C1: a JSON array of the only take ids the call is about, or "" for every take.
	takes string
	hook  func(query string)
}

func (r *txReader) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if r.hook != nil {
		r.hook(query)
	}
	return r.tx.QueryContext(ctx, query, args...)
}

// LiveMuses returns the JSON array of live (non-hidden) Muse ids a rule passes to its statement as `json_each(?)`. A rule never
// names the muses table: it takes the ids from the engine (C133, TestNextActions_NoMuseTableSQL). For a call about one Muse the
// array holds only that Muse, so the statement stays small. A Reader that is not the engine's yields no Muse.
func LiveMuses(q Reader) string {
	if r, ok := q.(*txReader); ok {
		return r.live
	}
	return "[]"
}

// SongScope returns the JSON array of Song ids a Song-level rule restricts its statement to, and "" when the call is about every
// Song; a rule passes it twice, as `? = ” OR id IN (SELECT value FROM json_each(?))`. A call about one Song then costs the
// statement's work for that Song, not for the library (the /context budget).
func SongScope(q Reader) string {
	if r, ok := q.(*txReader); ok {
		return r.songs
	}
	return ""
}

// TakeScope returns the JSON array of take ids a take-level rule restricts its statement to, and "" when the call is about every
// take; a rule passes it twice, as `? = ” OR col IN (SELECT value FROM json_each(?))`.
func TakeScope(q Reader) string {
	if r, ok := q.(*txReader); ok {
		return r.takes
	}
	return ""
}

type found struct {
	r     *Rule
	cands []Candidate // ordered by subject id, at most lim
	total int
}

func (f found) kept() int { return min(len(f.cands), PerCode) }

// admits reports whether the viewer's default audience includes a rule audience; a Filter audience overrides the default.
func admits(viewer auth.Principal, filter, rule Audience) bool {
	switch filter {
	case AudienceBoth:
		return rule == AudienceBoth
	case AudienceAgent:
		return rule == AudienceAgent || rule == AudienceBoth
	case AudienceHuman:
		return rule == AudienceHuman || rule == AudienceBoth
	}
	if viewer.ActsAsHuman() {
		return rule == AudienceHuman || rule == AudienceBoth
	}
	return rule == AudienceAgent || rule == AudienceBoth
}

func invalid(field, msg string) error {
	return problem.E(422, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

func subjectID(s Subject) string {
	if s.ID == nil {
		return ""
	}
	return *s.ID
}

func jsonIDs(ids []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(id))
	}
	b.WriteByte(']')
	return b.String()
}

// begin opens the read-only transaction one call runs in (GET never mutates) and resolves the live Muse ids; only (the Muse of a
// take or the subject of a filter) restricts them, and restrict with no id leaves none.
func (s *Service) begin(ctx context.Context, restrict bool, only string) (*txReader, func(), error) {
	if s.st == nil {
		return nil, nil, errors.ErrUnsupported
	}
	live := []string{}
	if s.muses != nil && !(restrict && only == "") {
		ids, err := s.muses.LiveIDs(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("nextactions: live muses: %w", err)
		}
		live = ids
	}
	if restrict {
		live = slices.DeleteFunc(slices.Clone(live), func(id string) bool { return id != only })
	}
	tx, err := s.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("nextactions: begin: %w", err)
	}
	return &txReader{tx: tx, live: jsonIDs(live), hook: s.hook}, func() { _ = tx.Rollback() }, nil
}

// evaluate runs the selected rules, one statement each. Candidates come back ordered by subject id.
func (s *Service) evaluate(ctx context.Context, q *txReader, rules []*Rule, lim int) ([]found, error) {
	out := make([]found, 0, len(rules))
	for _, r := range rules {
		cands, err := r.Find(ctx, q, lim)
		if err != nil {
			return nil, fmt.Errorf("nextactions: rule %s: %w", r.Code, err)
		}
		slices.SortStableFunc(cands, func(a, b Candidate) int { return cmp.Compare(subjectID(a.Subject), subjectID(b.Subject)) })
		f := found{r: r, cands: cands, total: len(cands)}
		if len(cands) > 0 {
			if n, ok := evInt(cands[0].Evidence, evTotal); ok && n > f.total {
				f.total = n
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func evInt(ev map[string]any, key string) (int, bool) {
	switch v := ev[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

// candidateRules selects the rules a call evaluates: the audience admits the viewer, the viewer holds the rule's scope, and the
// priority reaches the floor.
func candidateRules(viewer auth.Principal, aud Audience, minPriority int, only func(*Rule) bool) []*Rule {
	var out []*Rule
	for i := range Rules {
		r := &Rules[i]
		if !admits(viewer, aud, r.Audience) || r.Priority < minPriority || (r.MinScope != "" && !viewer.Has(r.MinScope)) {
			continue
		}
		if only != nil && !only(r) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// item is a candidate that survived the per-code cap, before rendering.
type item struct {
	r *Rule
	c Candidate
}

func lessItem(a, b item) int {
	return cmp.Or(cmp.Compare(b.r.Priority, a.r.Priority), cmp.Compare(a.r.Code, b.r.Code), cmp.Compare(subjectID(a.c.Subject), subjectID(b.c.Subject)))
}

// render resolves the titles the kept items need (one statement per kind) and renders text, hint and evidence.
func (s *Service) render(ctx context.Context, q *txReader, viewer auth.Principal, items []item) ([]Action, error) {
	var songs, records []string
	for _, it := range items {
		if it.c.Subject.ID == nil {
			continue
		}
		switch it.c.Subject.Kind {
		case SubjectSong:
			songs = append(songs, *it.c.Subject.ID)
		case SubjectRecord:
			records = append(records, *it.c.Subject.ID)
		}
	}
	n := &names{songs: map[string]string{}, records: map[string]string{}, human: viewer.ActsAsHuman()}
	if err := n.load(ctx, q, "songs", songs, n.songs); err != nil {
		return nil, err
	}
	if err := n.load(ctx, q, "records", records, n.records); err != nil {
		return nil, err
	}
	out := make([]Action, 0, len(items))
	for _, it := range items {
		a := Action{Code: it.r.Code, Priority: it.r.Priority, Audience: it.r.Audience, Subject: it.c.Subject, Text: it.r.Text(it.c, n), Evidence: publicEvidence(it.c.Evidence)}
		if it.r.Hint != nil {
			a.Hint = it.r.Hint(it.c, viewer)
		}
		out = append(out, a)
	}
	return out, nil
}

func publicEvidence(ev map[string]any) map[string]any {
	out := make(map[string]any, len(ev))
	for k, v := range ev {
		if !strings.HasPrefix(k, "_") {
			out[k] = v
		}
	}
	return out
}

// List answers list_next_actions (C139.1): every admitted rule is evaluated by one statement, at most PerCode items per code
// are kept (the rest is counted), and the items are paged by an opaque keyset cursor in (priority desc, code, subject id) order.
func (s *Service) List(ctx context.Context, viewer auth.Principal, f Filter) (Page, error) {
	limit := f.Limit
	switch {
	case limit == 0:
		limit = DefaultItems
	case limit < 1 || limit > MaxItems:
		return Page{}, invalid("limit", "limit must be between 1 and 50")
	}
	if f.MinPriority < 0 || f.MinPriority > 100 {
		return Page{}, invalid("min_priority", "min_priority must be between 0 and 100")
	}
	switch f.SubjectKind {
	case "", SubjectMuse, SubjectSong, SubjectRecord, SubjectTaxonomy, SubjectLibrary, SubjectTake:
	default:
		return Page{}, invalid("subject_kind", "unknown subject kind")
	}
	switch f.Audience {
	case "", AudienceAgent, AudienceHuman, AudienceBoth:
	default:
		return Page{}, invalid("audience", "audience must be agent, human or both")
	}
	if f.SubjectID != "" && f.SubjectKind == "" {
		return Page{}, invalid("subject_id", "subject_id needs subject_kind")
	}
	after, err := decodeCursor(f.Cursor)
	if err != nil {
		return Page{}, err
	}
	restrict := f.SubjectKind == SubjectMuse && f.SubjectID != ""
	q, done, err := s.begin(ctx, restrict, f.SubjectID)
	if err != nil {
		return Page{}, err
	}
	defer done()

	if f.SubjectKind == SubjectSong && f.SubjectID != "" {
		q.songs = jsonIDs([]string{f.SubjectID})
	}
	if f.SubjectKind == SubjectTake && f.SubjectID != "" {
		q.takes = jsonIDs([]string{f.SubjectID})
	}
	filtered := f.SubjectKind != ""
	lim := 4 * PerCode
	if filtered {
		lim = scanMax
	}
	fs, err := s.evaluate(ctx, q, candidateRules(viewer, f.Audience, f.MinPriority, nil), lim)
	if err != nil {
		return Page{}, err
	}
	var items []item
	page := Page{Items: []Action{}, Groups: []Group{}, GeneratedAt: store.FormatTime(s.now())}
	for _, fd := range fs {
		if filtered {
			fd.cands = slices.DeleteFunc(fd.cands, func(c Candidate) bool {
				return c.Subject.Kind != f.SubjectKind || (f.SubjectID != "" && subjectID(c.Subject) != f.SubjectID)
			})
			fd.total = len(fd.cands)
		}
		for _, c := range fd.cands[:fd.kept()] {
			items = append(items, item{fd.r, c})
		}
		page.Total += fd.total
		if more := fd.total - fd.kept(); more > 0 {
			page.Groups = append(page.Groups, Group{Code: fd.r.Code, More: more})
		}
	}
	slices.SortFunc(items, lessItem)
	slices.SortFunc(page.Groups, func(a, b Group) int {
		return cmp.Or(cmp.Compare(priorityOf(fs, b.Code), priorityOf(fs, a.Code)), cmp.Compare(a.Code, b.Code))
	})
	if after != nil {
		i := slices.IndexFunc(items, func(it item) bool { return cmp.Compare(cursorKey(it), *after) > 0 })
		if i < 0 {
			i = len(items)
		}
		items = items[i:]
	}
	if len(items) > limit {
		items = items[:limit]
		next := encodeCursor(cursorKey(items[limit-1]))
		page.NextCursor = &next
	}
	if page.Items, err = s.render(ctx, q, viewer, items); err != nil {
		return Page{}, err
	}
	return page, nil
}

func priorityOf(fs []found, code string) int {
	for _, f := range fs {
		if f.r.Code == code {
			return f.r.Priority
		}
	}
	return 0
}

// cursorKey orders items the way lessItem does: a string that sorts by (priority desc, code, subject id).
func cursorKey(it item) string {
	return fmt.Sprintf("%03d\x00%s\x00%s", 100-it.r.Priority, it.r.Code, subjectID(it.c.Subject))
}

func encodeCursor(key string) string { return base64.RawURLEncoding.EncodeToString([]byte(key)) }

func decodeCursor(c string) (*string, error) {
	if c == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || strings.Count(string(raw), "\x00") != 2 {
		return nil, problem.E(422, "INVALID_CURSOR", "the cursor is not valid", map[string]any{"field": "cursor"})
	}
	k := string(raw)
	return &k, nil
}

// ForTake answers the /context assembly: the rendered actions about this take (the take rules of M5-C1), its Song, its Muse or the
// Records that hold its Song, of priority at least ContextMuseMinPriority, best first, at most limit (C139.4, C257). A take without
// a Song has only the rules about the take itself.
func (s *Service) ForTake(ctx context.Context, viewer auth.Principal, trackID string, limit int) ([]Action, error) {
	if limit <= 0 {
		return []Action{}, nil
	}
	if s.st == nil {
		return nil, errors.ErrUnsupported
	}
	var songID string
	var museID sql.NullString
	err := s.st.R.QueryRowContext(ctx, `SELECT ts.song_id, s.muse_id FROM take_songs ts JOIN songs s ON s.id = ts.song_id
		WHERE ts.track_id = ? AND s.retired_at IS NULL`, trackID).Scan(&songID, &museID)
	hasSong := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("nextactions: song of take: %w", err)
	}
	q, done, err := s.begin(ctx, true, museID.String) // a Muse-less Song restricts to no Muse
	if err != nil {
		return nil, err
	}
	defer done()
	q.takes = jsonIDs([]string{trackID})
	subjects := map[subjKey]bool{{SubjectTake, trackID}: true}
	only := func(r *Rule) bool { return forTakeCodes[r.Code] }
	if hasSong {
		q.songs = jsonIDs([]string{songID})
		subjects[subjKey{SubjectSong, songID}] = true
		if museID.Valid {
			subjects[subjKey{SubjectMuse, museID.String}] = true
		}
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT rs.record_id FROM record_slots rs JOIN records r ON r.id = rs.record_id
			WHERE rs.song_id = ? AND rs.removed_at IS NULL AND r.hidden_at IS NULL`, songID)
		if err != nil {
			return nil, fmt.Errorf("nextactions: records of song: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			subjects[subjKey{SubjectRecord, id}] = true
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	} else {
		only = func(r *Rule) bool { return takeCodes[r.Code] }
	}
	rules := candidateRules(viewer, "", ContextMuseMinPriority, only)
	fs, err := s.evaluate(ctx, q, rules, scanMax)
	if err != nil {
		return nil, err
	}
	var items []item
	for _, fd := range fs {
		for _, c := range fd.cands {
			if subjects[subjKey{c.Subject.Kind, subjectID(c.Subject)}] {
				items = append(items, item{fd.r, c})
			}
		}
	}
	slices.SortFunc(items, lessItem)
	if len(items) > limit {
		items = items[:limit]
	}
	return s.render(ctx, q, viewer, items)
}

// subjKey is a (kind, id) pair used as a set key.
type subjKey struct{ Kind, ID string }
