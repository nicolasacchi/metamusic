// Package diction serves the Italian Diction Lab (M5b C344, C345): the karaoke lines of a take with word-level intelligibility,
// the personal lexicon of mangled words (computed on read from the stored Tier B rows, never stored) and the human marks that
// override the recogniser. It reads the rows internal/ears stores and writes only diction_marks and lexicon_overrides.
package diction

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options wires the service. Library is optional: without it the proportional basis has no timed sections.
type Options struct {
	Store   *store.Store
	Library *library.Service
	Now     func() time.Time
}

// Service is the Diction Lab.
type Service struct {
	o Options

	mu    sync.Mutex
	snap  *snapshot
	dirty bool
}

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// PageBytes is the size limit of a get_track_diction page (C345.1).
const PageBytes = 60000

// rebuildEvery is the shortest time between two rebuilds of the lexicon caused by new Tier B rows (C344.3). A write through this
// service invalidates at once.
const rebuildEvery = 5 * time.Second

// ErrNotFound is returned for an unknown track.
var ErrNotFound = library.ErrNotFound

func invalid(field, msg string) error {
	return problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

type calibLine struct {
	track, lang, mark string
}

// snapshot is the lexicon built from the stored rows.
type snapshot struct {
	key     string
	builtAt time.Time
	entries []LexiconEntry
	index   map[[2]string]int
	calib   []calibLine
}

func (s *Service) wired() error {
	if s.o.Store == nil {
		return errors.ErrUnsupported
	}
	return nil
}

// lexicon returns the cached lexicon, rebuilding it when its inputs changed: at once after a write through this service, at most
// every five seconds for new Tier B rows.
func (s *Service) lexicon(ctx context.Context) (*snapshot, error) {
	key, err := s.cacheKey(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.o.Now()
	if s.snap != nil && !s.dirty && s.snap.key == key {
		return s.snap, nil
	}
	if s.snap != nil && !s.dirty && now.Sub(s.snap.builtAt) < rebuildEvery {
		return s.snap, nil
	}
	tracks, err := s.allDiction(ctx)
	if err != nil {
		return nil, err
	}
	human, err := s.humanMarks(ctx)
	if err != nil {
		return nil, err
	}
	over, err := s.overrides(ctx)
	if err != nil {
		return nil, err
	}
	sn := &snapshot{key: key, builtAt: now, entries: BuildLexicon(tracks, human, over), index: map[[2]string]int{}}
	for i, e := range sn.entries {
		sn.index[[2]string{e.Lang, e.Word}] = i
	}
	for _, td := range tracks {
		for _, ln := range td.Lines {
			if m := human[td.TrackID][LineKey{ln.Section, ln.Line}]; m == MarkClear || m == MarkUnclear {
				sn.calib = append(sn.calib, calibLine{td.TrackID, td.Lang, m})
			}
		}
	}
	s.snap, s.dirty = sn, false
	return sn, nil
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// cursor is the opaque continuation of a page: an offset into the full list.
func encodeCursor(kind string, off int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + strconv.Itoa(off)))
}

func decodeCursor(kind, c string) (int, error) {
	if c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, invalid("cursor", "the cursor is not valid")
	}
	k, n, ok := strings.Cut(string(b), ":")
	off, perr := strconv.Atoi(n)
	if !ok || k != kind || perr != nil || off < 0 {
		return 0, invalid("cursor", "the cursor is not valid")
	}
	return off, nil
}

// Lines builds one page of the karaoke view of a take (C345.1). A take with neither a Tier B diction row nor timed sections
// answers 409 ANALYSIS_UNAVAILABLE.
func (s *Service) Lines(ctx context.Context, trackID, cursor string) (DictionLines, error) {
	if err := s.wired(); err != nil {
		return DictionLines{}, err
	}
	start, err := decodeCursor("d", cursor)
	if err != nil {
		return DictionLines{}, err
	}
	ti, err := s.trackInfo(ctx, trackID)
	if errors.Is(err, errNoTrack) {
		return DictionLines{}, ErrNotFound
	}
	if err != nil {
		return DictionLines{}, err
	}
	marksRows, err := s.trackMarks(ctx, trackID)
	if err != nil {
		return DictionLines{}, err
	}
	in := LinesInput{Sections: yue2.LyricsSections(ti.lyrics), Results: ti.results, Marks: markMap(marksRows), Thresholds: DefaultThresholds}
	if ti.results != nil || start == 0 { // the words are needed for the asr basis and for the hits of the first page
		in.Ref = tierb.RefWords(ti.lyrics)
	}
	if ti.results == nil && s.o.Library != nil {
		secs, err := s.o.Library.Sections(ctx, trackID)
		if err != nil {
			return DictionLines{}, err
		}
		for _, t := range secs.Timeline {
			in.Timeline = append(in.Timeline, TimedSection{Idx: t.Idx, StartS: t.StartS, EndS: t.EndS})
		}
	}
	basis, lines := BuildLines(in)
	if basis == BasisNone {
		return DictionLines{}, problem.E(http.StatusConflict, "ANALYSIS_UNAVAILABLE", "this take has no word timings and no timed sections",
			map[string]any{"analyzer": "tier_b", "hint": "a listening check gives word timings; an analyze of the take can add one"})
	}
	lang := ti.lang
	if ti.results == nil {
		lang = normLang(yue2.DetectLanguage(ti.lyrics))
	}
	out := DictionLines{TrackID: trackID, Basis: basis, Lang: lang, Thresholds: DefaultThresholds, Lines: []DictionLine{}, LexiconHits: []LexiconHit{}}
	if start > len(lines) {
		return DictionLines{}, invalid("cursor", "the cursor is past the end of the lines")
	}
	if start == 0 {
		if out.LexiconHits, err = s.hits(ctx, lang, in.Ref); err != nil {
			return DictionLines{}, err
		}
	}
	// The envelope without lines fixes the byte budget; every line then adds its own JSON and a comma. 80 bytes are kept for a cursor.
	env, err := json.Marshal(out)
	if err != nil {
		return DictionLines{}, err
	}
	budget := PageBytes - len(env) - 80
	n := start
	for ; n < len(lines); n++ {
		b, err := json.Marshal(lines[n])
		if err != nil {
			return DictionLines{}, err
		}
		if budget < len(b)+1 && n > start {
			break
		}
		budget -= len(b) + 1
		out.Lines = append(out.Lines, lines[n])
	}
	if n < len(lines) {
		c := encodeCursor("d", n)
		out.NextCursor, out.Truncated = &c, true
	}
	return out, nil
}

// hits are the lexicon words of this take that were mangled before, most mangled first, at most ten.
func (s *Service) hits(ctx context.Context, lang string, ref []tierb.RefWord) ([]LexiconHit, error) {
	sn, err := s.lexicon(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []LexiconHit
	for _, w := range ref {
		if seen[w.Fold] {
			continue
		}
		seen[w.Fold] = true
		if i, ok := sn.index[[2]string{lang, w.Fold}]; ok {
			if e := sn.entries[i]; e.Mangled > 0 && e.Status == "active" {
				out = append(out, LexiconHit{Word: e.Word, TypicalHeard: e.TypicalHeard, Attempts: e.Attempts, Mangled: e.Mangled})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Mangled != out[j].Mangled {
			return out[i].Mangled > out[j].Mangled
		}
		return out[i].Word < out[j].Word
	})
	return out[:min(len(out), 10)], nil
}

// LexiconQuery filters the lexicon listing.
type LexiconQuery struct {
	Lang   string
	Risk   *bool
	Q      string
	Limit  int
	Cursor string
}

// Lexicon lists the personal lexicon (C345.3) with the calibration progress.
func (s *Service) Lexicon(ctx context.Context, q LexiconQuery) (LexiconPage, error) {
	if err := s.wired(); err != nil {
		return LexiconPage{}, err
	}
	off, err := decodeCursor("l", q.Cursor)
	if err != nil {
		return LexiconPage{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	sn, err := s.lexicon(ctx)
	if err != nil {
		return LexiconPage{}, err
	}
	needle := tierb.Fold(q.Q)
	var match []LexiconEntry
	for _, e := range sn.entries {
		if (q.Lang != "" && e.Lang != q.Lang) || (q.Risk != nil && e.Risk != *q.Risk) || (needle != "" && !strings.Contains(e.Word, needle)) {
			continue
		}
		match = append(match, e)
	}
	out := LexiconPage{Entries: []LexiconEntry{}, Total: len(match), Calibration: Calibration{Lang: "all"}}
	if q.Lang != "" {
		out.Calibration.Lang = q.Lang
	}
	tracks := map[string]bool{}
	for _, c := range sn.calib {
		if q.Lang != "" && c.lang != q.Lang {
			continue
		}
		out.Calibration.LinesMarked++
		tracks[c.track] = true
		if c.mark == MarkClear {
			out.Calibration.Clear++
		} else {
			out.Calibration.Unclear++
		}
	}
	out.Calibration.TracksMarked = len(tracks)
	if off > len(match) {
		return LexiconPage{}, invalid("cursor", "the cursor is past the end of the entries")
	}
	end := min(off+limit, len(match))
	out.Entries = append(out.Entries, match[off:end]...)
	if end < len(match) {
		c := encodeCursor("l", end)
		out.NextCursor = &c
	}
	return out, nil
}

// nextStamp is the updated_at of a row: the clock in milliseconds, bumped by one millisecond when the row's previous stamp is not
// older, so it strictly increases per row (C344.1).
func (s *Service) nextStamp(prev string) string {
	now := s.o.Now().UTC().Truncate(time.Millisecond)
	if prev != "" {
		if p, err := store.ParseTime(prev); err == nil && !now.After(p) {
			now = p.Add(time.Millisecond)
		}
	}
	return store.FormatTime(now)
}

// EntryPatch is the body of update_lexicon_entry; nil fields are left as they are. An empty note removes it.
type EntryPatch struct {
	Status *string
	Note   *string
}

// UpdateEntry sets the status and note of a lexicon word (C345.3). The word must be in the lexicon or be a fold of at least three
// runes. It answers the entry as the lexicon now shows it (zero attempts for a word never heard).
func (s *Service) UpdateEntry(ctx context.Context, p auth.Principal, lang, word string, patch EntryPatch) (LexiconEntry, error) {
	if err := s.wired(); err != nil {
		return LexiconEntry{}, err
	}
	if lang = strings.ToLower(lang); normLang(lang) != lang {
		return LexiconEntry{}, invalid("lang", "lang must be an ISO 639 code of two or three letters, or und")
	}
	fold := tierb.Fold(word)
	if fold == "" || strings.Contains(fold, " ") || len([]rune(fold)) > 64 {
		return LexiconEntry{}, invalid("word", "word must be a single word of at most 64 letters")
	}
	if patch.Status == nil && patch.Note == nil {
		return LexiconEntry{}, invalid("body", "give a status or a note")
	}
	if patch.Status != nil && *patch.Status != "active" && *patch.Status != "ignored" {
		return LexiconEntry{}, invalid("status", "status must be active or ignored")
	}
	if patch.Note != nil && len([]rune(*patch.Note)) > 500 {
		return LexiconEntry{}, invalid("note", "note is at most 500 characters")
	}
	sn, err := s.lexicon(ctx)
	if err != nil {
		return LexiconEntry{}, err
	}
	_, known := sn.index[[2]string{lang, fold}]
	if !known && len([]rune(fold)) < MinWordRunes {
		return LexiconEntry{}, problem.E(http.StatusNotFound, "NOT_FOUND", "no such word in the lexicon", map[string]any{"lang": lang, "word": fold})
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return LexiconEntry{}, fmt.Errorf("diction: lexicon: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	status, prevAt := "active", ""
	var note *string
	var ps, pn, pa sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status, note, updated_at FROM lexicon_overrides WHERE lang = ? AND word_fold = ?`, lang, fold).Scan(&ps, &pn, &pa); err == nil {
		status, prevAt = ps.String, pa.String
		if pn.Valid {
			note = &pn.String
		}
	}
	if patch.Status != nil {
		status = *patch.Status
	}
	if patch.Note != nil {
		if note = patch.Note; *note == "" {
			note = nil
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lexicon_overrides(lang, word_fold, status, note, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(lang, word_fold) DO UPDATE SET status = excluded.status, note = excluded.note, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		lang, fold, status, note, p.Key(), s.nextStamp(prevAt)); err != nil {
		return LexiconEntry{}, fmt.Errorf("diction: lexicon override: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return LexiconEntry{}, fmt.Errorf("diction: lexicon commit: %w", err)
	}
	s.invalidate()
	sn, err = s.lexicon(ctx)
	if err != nil {
		return LexiconEntry{}, err
	}
	if i, ok := sn.index[[2]string{lang, fold}]; ok {
		return sn.entries[i], nil
	}
	return LexiconEntry{Lang: lang, Word: fold, Status: status, Note: note, TypicalHeard: []HeardCount{}, Examples: []LexiconExample{}}, nil
}
