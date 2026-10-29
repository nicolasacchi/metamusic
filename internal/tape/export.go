package tape

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Export rules and the input address of a tape (C128.1, C128.5). Nothing here reads Muse text: the snapshot carries it only
// when the curation flag is on, and no title or Muse line is ever stored on a tapes row (the chapter names live in the file
// and the key).

const (
	// KeyVersion starts every input key; a change to what the key covers bumps it.
	KeyVersion = "mm-tape/1"
	// TargetMinInput and TargetMaxInput bound a caller's target in LUFS (the plan clamps its own automatic target tighter).
	TargetMinInput = -30.0
	TargetMaxInput = -9.0
	// coverSize is the pixel size of the front cover a tape carries.
	coverSize = 512
)

var liveStates = `('queued','running','ready')`

func errNotFound(id string) error {
	return problem.E(404, "NOT_FOUND", "tape not found", map[string]any{"tape_id": id})
}

func errRecordNotFound(id string) error {
	return problem.E(404, "NOT_FOUND", "record not found", map[string]any{"record_id": id})
}

// paramsDoc is tapes.params_json: what the caller asked for plus the Muse flag the snapshot honoured.
type paramsDoc struct {
	TargetLUFS   *float64 `json:"target_lufs"`
	IncludeLiner bool     `json:"include_liner"`
	MuseNames    bool     `json:"muse_names"`
}

type measuredDoc struct {
	LUFS         float64 `json:"lufs"`
	TruePeakDBTP float64 `json:"true_peak_dbtp"`
}

type planItemDoc struct {
	SlotID        string       `json:"slot_id"`
	TakeID        string       `json:"take_id"`
	Position      int          `json:"position"`
	MasterSHA256  string       `json:"master_sha256"`
	GainCDB       int          `json:"gain_cdb"`
	Status        string       `json:"status"`
	Measured      *measuredDoc `json:"measured"`
	PredictedLUFS *float64     `json:"predicted_lufs"`
	Lossy         bool         `json:"lossy"`
	Resampled     bool         `json:"resampled"`
	Upmixed       bool         `json:"upmixed"`
	Downmixed     bool         `json:"downmixed"`
	DurationS     float64      `json:"duration_s"`
}

type chapterDoc struct {
	Index       int    `json:"index"`
	SlotID      string `json:"slot_id"`
	TakeID      string `json:"take_id"`
	StartSample int64  `json:"start_sample"`
	Samples     int64  `json:"samples"`
	StartMS     int64  `json:"start_ms"`
}

type warningDoc struct {
	SlotID string `json:"slot_id"`
	Code   string `json:"code"`
}

type spreadDoc struct {
	Before float64 `json:"before"`
	After  float64 `json:"after"`
}

// planDoc is tapes.plan_json (mm.tape/1): the items, gains and flags; chapters are added when the tape is ready. No titles.
type planDoc struct {
	Def              string        `json:"def"`
	TargetLUFS       float64       `json:"target_lufs"`
	SpreadLU         *spreadDoc    `json:"spread_lu"`
	Items            []planItemDoc `json:"items"`
	Chapters         []chapterDoc  `json:"chapters,omitempty"`
	Warnings         []warningDoc  `json:"warnings"`
	AcceptedWarnings bool          `json:"accepted_warnings"`
}

// prepared is a snapshot turned into a plan, a job's inputs and the input key.
type prepared struct {
	snap     Snapshot
	items    []PlannedItem
	target   float64
	chapters []string
	tags     []flacmeta.Comment
	coverKey string
	doc      planDoc
	key      string
	lossy    int
}

// Export snapshots a published Record and queues (or reuses) a tape for it (C128.1).
func (s *Service) Export(ctx context.Context, p auth.Principal, recordID string, in ExportInput) (Tape, error) {
	if s.o.Store == nil || s.o.Source == nil {
		return Tape{}, problem.E(503, "UNAVAILABLE", "tape is not available", map[string]any{"component": "tape"})
	}
	status, revision, err := s.recordState(ctx, recordID)
	if err != nil {
		return Tape{}, err
	}
	if status != "published" {
		return Tape{}, errNotPublished(status)
	}
	if in.Revision != nil && *in.Revision != revision {
		return Tape{}, problem.E(409, "STALE_REVISION", "the record changed; re-read it and retry",
			map[string]any{"revision": revision, "status": status})
	}
	snap, err := s.o.Source.Snapshot(ctx, recordID, SnapOptions{IncludeLiner: in.IncludeLiner})
	if err != nil {
		return Tape{}, passThrough(err)
	}
	if err := checkLimits(snap); err != nil {
		return Tape{}, err
	}
	var target *float64
	if in.TargetLUFS != nil {
		t := *in.TargetLUFS
		if math.IsNaN(t) || t < TargetMinInput || t > TargetMaxInput {
			return Tape{}, problem.E(422, "VALIDATION_FAILED", "target_lufs must be between -30 and -9",
				map[string]any{"field": "target_lufs"})
		}
		r := float64(roundTenthsInt(t)) / tenthStep
		target = &r
	}
	if in.AcceptWarnings && !p.ActsAsHuman() {
		return Tape{}, problem.E(403, "HUMAN_ONLY", "only the operator can accept review warnings",
			map[string]any{"hint": "ask the operator to export this tape"})
	}
	if s.o.FlacVersion == "" {
		return Tape{}, problem.E(503, "UNAVAILABLE", "flac is not available", map[string]any{"component": "flac"})
	}
	snap.Items = s.measure(ctx, snap.Items)
	params := paramsDoc{TargetLUFS: target, IncludeLiner: in.IncludeLiner, MuseNames: snap.MuseNames}
	pr, err := s.prepare(ctx, snap, params, in.AcceptWarnings)
	if err != nil {
		return Tape{}, err
	}
	if len(snap.Warnings) > 0 && !in.AcceptWarnings {
		slots := make([]map[string]string, len(snap.Warnings))
		for i, w := range snap.Warnings {
			slots[i] = map[string]string{"slot_id": w.SlotID, "code": w.Code}
		}
		return Tape{}, problem.E(409, ErrNeedsReview, "slots need review before a tape is exported", map[string]any{"slots": slots})
	}
	id, reused, err := s.insert(ctx, p, pr, params)
	if err != nil {
		return Tape{}, err
	}
	t, err := s.Get(ctx, id)
	if err != nil {
		return Tape{}, err
	}
	t.Reused = reused
	if !reused {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return t, nil
}

func errNotPublished(status string) error {
	return problem.E(409, "RECORD_NOT_PUBLISHED", "only a published record can be exported as a tape", map[string]any{"status": status})
}

// passThrough keeps problem errors of the Source as they are and wraps anything else.
func passThrough(err error) error {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return err
	}
	return fmt.Errorf("tape: snapshot: %w", err)
}

// recordState reads the status and revision the cheap checks of Export need.
func (s *Service) recordState(ctx context.Context, id string) (status string, revision int, err error) {
	err = s.o.Store.R.QueryRowContext(ctx, `SELECT status, revision FROM records WHERE id = ?`, id).Scan(&status, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, errRecordNotFound(id)
	}
	if err != nil {
		return "", 0, fmt.Errorf("tape: record: %w", err)
	}
	return status, revision, nil
}

// checkLimits applies RECORD_EMPTY and TAPE_TOO_LONG to a snapshot.
func checkLimits(snap Snapshot) error {
	if len(snap.Items) == 0 {
		return problem.E(422, "RECORD_EMPTY", "the record has no playable take", map[string]any{"record_id": snap.RecordID})
	}
	var dur float64
	for _, it := range snap.Items {
		dur += it.DurationS
	}
	if len(snap.Items) > MaxItems || dur > MaxDurationS {
		return problem.E(422, ErrTooLong, "a tape holds at most 60 items and two hours",
			map[string]any{"items": len(snap.Items), "duration_s": math.Round(dur), "max_items": MaxItems, "max_duration_s": MaxDurationS})
	}
	return nil
}

// measure asks Loudness for the Takes the snapshot has no measure for; a failure only leaves the items unmeasured.
func (s *Service) measure(ctx context.Context, in []Item) []Item {
	items := append([]Item(nil), in...)
	var missing []string
	for _, it := range items {
		if !it.Measure.OK {
			missing = append(missing, it.TakeID)
		}
	}
	if len(missing) == 0 || s.o.Loudness == nil {
		return items
	}
	got, err := s.o.Loudness.Ensure(ctx, missing)
	if err != nil {
		s.log.Warn("tape: loudness analysis failed", "err", err.Error())
	}
	for i, it := range items {
		if m, ok := got[it.TakeID]; ok && !it.Measure.OK {
			items[i].Measure = m
		}
	}
	return items
}

// roundTenthsInt rounds LU to whole tenths, half away from zero.
func roundTenthsInt(x float64) int { return int(math.Round(x * tenthStep)) }

// prepare turns a snapshot (measures already filled in) into the plan, the cover key and the input key. The runner calls it
// again with a fresh snapshot: a key that differs from the stored one means the inputs changed (TAPE_SOURCE_CHANGED).
func (s *Service) prepare(ctx context.Context, snap Snapshot, params paramsDoc, accepted bool) (prepared, error) {
	ms := make([]Measure, len(snap.Items))
	for i, it := range snap.Items {
		ms[i] = it.Measure
	}
	target, plans := PlanGains(ms, params.TargetLUFS)
	pr := prepared{snap: snap, target: target, items: make([]PlannedItem, len(snap.Items)), chapters: make([]string, len(snap.Items))}
	doc := planDoc{Def: "mm.tape/1", TargetLUFS: target, Items: make([]planItemDoc, len(snap.Items)), Warnings: []warningDoc{},
		AcceptedWarnings: accepted && len(snap.Warnings) > 0}
	var before, after []float64
	for i, it := range snap.Items {
		g := plans[i]
		pi := PlannedItem{Item: it, Gain: g, Resampled: it.SampleRate != Rate, Upmixed: it.Channels == 1, Downmixed: it.Channels > 2}
		pr.items[i] = pi
		pr.chapters[i] = ChapterName(it.Title, it.Side, it.Lossy)
		if it.Lossy {
			pr.lossy++
		}
		d := planItemDoc{SlotID: it.SlotID, TakeID: it.TakeID, Position: it.Position, MasterSHA256: it.MasterSHA256, GainCDB: g.GainCDB,
			Status: string(g.Status), PredictedLUFS: g.PredictedLUFS, Lossy: it.Lossy, Resampled: pi.Resampled, Upmixed: pi.Upmixed,
			Downmixed: pi.Downmixed, DurationS: it.DurationS}
		if it.Measure.OK {
			d.Measured = &measuredDoc{LUFS: it.Measure.LUFS, TruePeakDBTP: it.Measure.TruePeakDBTP}
		}
		doc.Items[i] = d
		if g.PredictedLUFS != nil && (g.Status == GainMatched || g.Status == GainPeakLimited) {
			before = append(before, it.Measure.LUFS)
			after = append(after, *g.PredictedLUFS)
		}
	}
	if len(before) > 0 {
		doc.SpreadLU = &spreadDoc{Before: spreadOf(before), After: spreadOf(after)}
	}
	for _, w := range snap.Warnings {
		doc.Warnings = append(doc.Warnings, warningDoc(w))
	}
	pr.doc = doc
	pr.tags = BuildTags(TagsInput{Snapshot: snap, Items: pr.items, TargetLUFS: target, IncludeLiner: params.IncludeLiner})
	if s.o.Covers != nil {
		_, key, err := s.o.Covers.EnsureMosaic(ctx, snap.CoverInput, coverSize)
		if err != nil {
			return prepared{}, fmt.Errorf("tape: cover: %w", err)
		}
		pr.coverKey = key
	}
	pr.key = inputKey(s.o.FlacVersion, s.o.BuildVersion, pr, params)
	return pr, nil
}

// spreadOf is the largest minus the smallest value, rounded to 0.1 LU.
func spreadOf(v []float64) float64 {
	lo, hi := v[0], v[0]
	for _, x := range v[1:] {
		lo, hi = min(lo, x), max(hi, x)
	}
	return math.Round((hi-lo)*tenthStep) / tenthStep
}

type keyItem struct {
	MasterSHA256 string `json:"master_sha256"`
	GainCDB      int    `json:"gain_cdb"`
	Resampled    bool   `json:"resampled"`
	Upmixed      bool   `json:"upmixed"`
	Downmixed    bool   `json:"downmixed"`
}

type keyTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type keyDoc struct {
	Items        []keyItem `json:"items"`
	TargetLUFS   float64   `json:"target_lufs"`
	IncludeLiner bool      `json:"include_liner"`
	MuseNames    bool      `json:"muse_names"`
	Tags         []keyTag  `json:"tags"`
	Chapters     []string  `json:"chapters"`
	Description  string    `json:"description"`
	Cover        string    `json:"cover"`
	Vendor       string    `json:"vendor"`
}

// inputKey is the input address of a tape (C128.5): everything that decides the bytes of the file and nothing else. It
// excludes the Record revision, the tape id and the principal, so two revisions with identical audio and metadata are one
// file, and the warnings the caller accepted do not matter. The tags are sorted; the plan's resolved target (rounded to
// 0.1 LU) is used, so -14.04 and -14.0 are one key.
func inputKey(flacVersion, buildVersion string, pr prepared, params paramsDoc) string {
	k := keyDoc{TargetLUFS: pr.target, IncludeLiner: params.IncludeLiner, MuseNames: pr.snap.MuseNames, Chapters: pr.chapters,
		Description: pr.snap.Description, Cover: pr.coverKey, Vendor: "metamusic " + buildVersion}
	for _, it := range pr.items {
		k.Items = append(k.Items, keyItem{MasterSHA256: it.Item.MasterSHA256, GainCDB: it.Gain.GainCDB, Resampled: it.Resampled,
			Upmixed: it.Upmixed, Downmixed: it.Downmixed})
	}
	k.Tags = sortedTags(pr.tags)
	b, _ := json.Marshal(k) // plain structs of strings and numbers: no error
	sum := sha256.Sum256([]byte(KeyVersion + "\n" + flacVersion + "\n" + string(b)))
	return hex.EncodeToString(sum[:])
}

func sortedTags(in []flacmeta.Comment) []keyTag {
	out := make([]keyTag, len(in))
	for i, c := range in {
		out[i] = keyTag{Key: c.Key, Value: c.Value}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// insert reuses the live row with the key or, after the quotas, inserts a queued one. One write transaction.
func (s *Service) insert(ctx context.Context, p auth.Principal, pr prepared, params paramsDoc) (id string, reused bool, err error) {
	planJSON, _ := json.Marshal(pr.doc)
	paramsJSON, _ := json.Marshal(params)
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		id, reused = "", false
		var status string
		var revision int
		if err := tx.QueryRowContext(ctx, `SELECT status, revision FROM records WHERE id = ?`, pr.snap.RecordID).Scan(&status, &revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errRecordNotFound(pr.snap.RecordID)
			}
			return err
		}
		if status != "published" {
			return errNotPublished(status)
		}
		switch err := tx.QueryRowContext(ctx, `SELECT id FROM tapes WHERE key = ? AND state IN `+liveStates, pr.key).Scan(&id); {
		case err == nil:
			reused = true
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tapes WHERE record_id = ? AND state IN `+liveStates, pr.snap.RecordID).Scan(&live); err != nil {
			return err
		}
		if live >= MaxLivePerRecord {
			return problem.E(409, ErrQuota, "this record already has the most live tapes",
				map[string]any{"scope": "record", "limit": MaxLivePerRecord})
		}
		var ready int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM tapes WHERE state = 'ready'`).Scan(&ready); err != nil {
			return err
		}
		if ready >= MaxReadyBytes {
			return problem.E(409, ErrQuota, "the ready tapes fill the disk quota; wait for one to expire",
				map[string]any{"scope": "total", "limit_bytes": int64(MaxReadyBytes)})
		}
		id = s.newID()
		if pr.snap.Revision >= 1 {
			revision = pr.snap.Revision // the revision the snapshot was read at
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO tapes(id, record_id, record_revision, key, state, params_json, plan_json, contains_lossy,
			lossy_sources, principal_id, created_at) VALUES (?,?,?,?, 'queued', ?,?,?,?,?,?)`,
			id, pr.snap.RecordID, revision, pr.key, string(paramsJSON), string(planJSON), b2i(pr.lossy > 0), pr.lossy, p.Key(),
			store.FormatTime(s.now()))
		return err
	})
	return id, reused, err
}

func (s *Service) newID() string {
	if s.o.NewID != nil {
		return s.o.NewID()
	}
	return fmt.Sprintf("tape-%d", s.now().UnixNano())
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isBusy reports a SQLITE_BUSY or locked error.
func isBusy(err error) bool {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		if c := coded.Code() & 0xff; c == 5 || c == 6 {
			return true
		}
	}
	m := err.Error()
	return strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked")
}

// busyRetries is the bound of the SQLITE_BUSY retry (20 ms x 2^n), as in C119.1 note 10.
const busyRetries = 5

// writeTx runs fn in one write transaction on the writer pool (BEGIN IMMEDIATE through _txlock) and retries the whole call
// when it met SQLITE_BUSY; fn must therefore be repeatable.
func (s *Service) writeTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	for n := 0; ; n++ {
		err := s.writeOnce(ctx, fn)
		if err == nil || !isBusy(err) || n >= busyRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond << n):
		}
	}
}

func (s *Service) writeOnce(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
