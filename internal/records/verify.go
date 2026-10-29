package records

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Verify implements the invariants R1 to R9 of C122.5 as read-only SQL over one snapshot. It never writes: `library verify`
// prints every finding and exits 1 on any of severity "error"; "warn" is printed only. It is also the oracle of the property
// test. Anomaly.Subject is the id of the offending row; Detail holds ids and numbers only.

// The anomaly codes, with their invariant.
const (
	CodeOrderMismatch      = "order_mismatch"       // R1 error
	CodeDuplicateSongSlot  = "duplicate_song_slot"  // R1 error
	CodeSlotSongMissing    = "slot_song_missing"    // R2 error
	CodeRetiredSong        = "retired_song"         // R2 warn
	CodePinNotInSong       = "pin_not_in_song"      // R3 error
	CodeChangesChain       = "changes_chain_broken" // R4 error
	CodeChangesNoCreated   = "changes_not_created"  // R4 error
	CodeReplayMismatch     = "changes_replay"       // R5 error
	CodeCoverSeed          = "cover_seed"           // R6 error
	CodeFirstPublished     = "first_published"      // R6 error
	CodePublishedEmpty     = "published_empty"      // R6 warn
	CodeCrateItem          = "crate_item"           // R7 error
	CodeCrateFilterInvalid = "crate_filter_invalid" // R7 warn
	CodeTapeKey            = "tape_key"             // R8 error
	CodeTapeLease          = "tape_lease"           // R8 error
	CodeSettingKey         = "setting_key"          // R9 error
	CodeSettingValue       = "setting_value"        // R9 error
)

// tapeLeaseTTL is the runner lease of a running tape (tape.LeaseTTL, 60 s; a test pins the equality: this package imports
// no other L2 package, C118).
const tapeLeaseTTL = 60 * time.Second

// curationKeys are the settings keys under curation. (curation.Keys; a test pins the equality) and the two that hold a flag.
var (
	curationKeys  = []string{"curation.export_muse_names", "curation.subsonic_muse_names", "curation.updated_at", "curation.updated_by"}
	curationFlags = []string{"curation.export_muse_names", "curation.subsonic_muse_names"}
)

// CrateFilterCheck validates a smart Crate's stored filter under the current vocabulary; nil means valid.
type CrateFilterCheck func(subject string, filter json.RawMessage) error

var crateFilterCheck atomic.Pointer[CrateFilterCheck]

// SetCrateFilterCheck installs the validator behind the R7 warning crate_filter_invalid. records imports no other L2
// package, so the composition root hands in crates.ParseFilter; without it the warning is not raised.
func SetCrateFilterCheck(f CrateFilterCheck) {
	if f == nil {
		crateFilterCheck.Store(nil)
		return
	}
	crateFilterCheck.Store(&f)
}

// Verify checks the invariants R1 to R9 (C122.5), read-only.
func (s *Service) Verify(ctx context.Context) ([]Anomaly, error) {
	tx, err := s.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("records: verify: %w", err)
	}
	defer tx.Rollback()
	v := &verifier{ctx: ctx, q: tx, now: s.now()}
	for _, check := range []func() error{v.r1, v.r2, v.r3, v.r4, v.r5, v.r6, v.r7, v.r8, v.r9} {
		if err := check(); err != nil {
			return nil, err
		}
	}
	return v.out, nil
}

type verifier struct {
	ctx context.Context
	q   querier
	now time.Time
	out []Anomaly
}

func (v *verifier) add(code, severity, subject, format string, args ...any) {
	v.out = append(v.out, Anomaly{Code: code, Severity: severity, Subject: subject, Detail: fmt.Sprintf(format, args...)})
}

// each runs a query and calls fn per row; the rows are closed before it returns.
func (v *verifier) each(query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := v.q.QueryContext(v.ctx, query, args...)
	if err != nil {
		return fmt.Errorf("records: verify: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// r1: order_json lists exactly the live slots, each once; no live slot twice for one Song.
func (v *verifier) r1() error {
	live := map[string][]string{}
	if err := v.each(`SELECT record_id, id FROM record_slots WHERE removed_at IS NULL ORDER BY record_id, id`, nil, func(r *sql.Rows) error {
		var rec, id string
		if err := r.Scan(&rec, &id); err != nil {
			return err
		}
		live[rec] = append(live[rec], id)
		return nil
	}); err != nil {
		return err
	}
	if err := v.each(`SELECT id, order_json FROM records ORDER BY id`, nil, func(r *sql.Rows) error {
		var id, raw string
		if err := r.Scan(&id, &raw); err != nil {
			return err
		}
		var order []string
		if err := json.Unmarshal([]byte(raw), &order); err != nil {
			v.add(CodeOrderMismatch, "error", id, "order_json is not a list of ids")
			return nil //nolint:nilerr // the finding is the report
		}
		got := slices.Clone(order)
		slices.Sort(got)
		if !slices.Equal(got, live[id]) || len(slices.Compact(got)) != len(got) {
			v.add(CodeOrderMismatch, "error", id, "order lists %d slots, the Record has %d live", len(order), len(live[id]))
		}
		return nil
	}); err != nil {
		return err
	}
	return v.each(`SELECT record_id, song_id, COUNT(*) FROM record_slots WHERE removed_at IS NULL GROUP BY record_id, song_id HAVING COUNT(*) > 1`, nil, func(r *sql.Rows) error {
		var rec, song string
		var n int
		if err := r.Scan(&rec, &song, &n); err != nil {
			return err
		}
		v.add(CodeDuplicateSongSlot, "error", rec, "song %s is in %d live slots", song, n)
		return nil
	})
}

// r2: a live slot's Song exists; a live slot on a retired Song is a warning (the Syncer follows it).
func (v *verifier) r2() error {
	return v.each(`SELECT s.id, s.song_id, so.id IS NULL, so.retired_at IS NOT NULL
		FROM record_slots s LEFT JOIN songs so ON so.id = s.song_id WHERE s.removed_at IS NULL ORDER BY s.id`, nil, func(r *sql.Rows) error {
		var id, song string
		var missing, retired bool
		if err := r.Scan(&id, &song, &missing, &retired); err != nil {
			return err
		}
		switch {
		case missing:
			v.add(CodeSlotSongMissing, "error", id, "song %s does not exist", song)
		case retired:
			v.add(CodeRetiredSong, "warn", id, "song %s is retired; the syncer follows it", song)
		}
		return nil
	})
}

// r3: pinned_take_id belongs to the slot's Song.
func (v *verifier) r3() error {
	return v.each(`SELECT s.id, s.pinned_take_id, s.song_id FROM record_slots s
		WHERE s.removed_at IS NULL AND s.pinned_take_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM take_songs t WHERE t.track_id = s.pinned_take_id AND t.song_id = s.song_id) ORDER BY s.id`, nil, func(r *sql.Rows) error {
		var id, take, song string
		if err := r.Scan(&id, &take, &song); err != nil {
			return err
		}
		v.add(CodePinNotInSong, "error", id, "pinned take %s is not a take of song %s", take, song)
		return nil
	})
}

// r4: per Record the changelog seq is 1..n and the first row is `created`.
func (v *verifier) r4() error {
	return v.each(`SELECT r.id, COUNT(c.seq), COALESCE(MAX(c.seq), 0), COALESCE((SELECT kind FROM record_changes WHERE record_id = r.id AND seq = 1), '')
		FROM records r LEFT JOIN record_changes c ON c.record_id = r.id GROUP BY r.id ORDER BY r.id`, nil, func(r *sql.Rows) error {
		var id, first string
		var n, highest int
		if err := r.Scan(&id, &n, &highest, &first); err != nil {
			return err
		}
		if n != highest {
			v.add(CodeChangesChain, "error", id, "%d rows, highest seq %d", n, highest)
		}
		if first != KindCreated {
			v.add(CodeChangesNoCreated, "error", id, "the first row is %q", first)
		}
		return nil
	})
}

// takeKinds are the changelog kinds that carry a Take; R5 folds them per slot.
var takeKinds = []string{KindSlotAdded, KindSlotPinned, KindSlotUnpinned, KindSlotUpgraded, KindSlotFilled, KindSlotEmptied, KindSlotSongFollowed}

type slotState struct {
	record     string
	expected   *string // the Take the slot resolves to now
	unconsumed bool    // a floating slot with events (or a retired Song) the Syncer has not consumed
}

// r5: per live slot, the take-bearing rows chain (each from equals the previous to; the first is slot_added) and, unless
// events are unconsumed, the last to equals the slot's current resolved Take: the replay of the changelog is the state.
func (v *verifier) r5() error {
	states := map[string]slotState{}
	var order []string
	if err := v.each(`SELECT s.id, s.record_id, s.pinned_take_id, p.take_id, s.pinned_take_id IS NULL AND (
			EXISTS (SELECT 1 FROM pick_events e WHERE e.song_id = s.song_id AND e.seq > s.synced_seq) OR so.retired_at IS NOT NULL)
		FROM record_slots s LEFT JOIN song_picks p ON p.song_id = s.song_id LEFT JOIN songs so ON so.id = s.song_id
		WHERE s.removed_at IS NULL ORDER BY s.id`, nil, func(r *sql.Rows) error {
		var id, rec string
		var pinned, pick *string
		var unconsumed bool
		if err := r.Scan(&id, &rec, &pinned, &pick, &unconsumed); err != nil {
			return err
		}
		exp := pick
		if pinned != nil {
			exp = pinned
		}
		states[id] = slotState{record: rec, expected: exp, unconsumed: unconsumed}
		order = append(order, id)
		return nil
	}); err != nil {
		return err
	}
	type fold struct {
		last   *string
		n      int
		broken bool
	}
	folds := map[string]*fold{}
	args := make([]any, len(takeKinds))
	for i, k := range takeKinds {
		args[i] = k
	}
	if err := v.each(`SELECT slot_id, kind, from_take_id, to_take_id FROM record_changes WHERE slot_id IS NOT NULL AND kind IN (`+placeholders(len(takeKinds))+`)
		ORDER BY slot_id, seq`, args, func(r *sql.Rows) error {
		var slot, kind string
		var from, to *string
		if err := r.Scan(&slot, &kind, &from, &to); err != nil {
			return err
		}
		st, live := states[slot]
		if !live {
			return nil
		}
		f := folds[slot]
		if f == nil {
			f = &fold{}
			folds[slot] = f
			if kind != KindSlotAdded {
				f.broken = true
				v.add(CodeReplayMismatch, "error", slot, "the first take row is %q, not slot_added (record %s)", kind, st.record)
			}
		} else if !sameTake(f.last, from) && !f.broken {
			f.broken = true
			v.add(CodeReplayMismatch, "error", slot, "row %d (%s) starts from %s, the previous row ended at %s", f.n+1, kind, takeStr(from), takeStr(f.last))
		}
		f.n++
		f.last = to
		return nil
	}); err != nil {
		return err
	}
	for _, id := range order {
		st, f := states[id], folds[id]
		switch {
		case f == nil:
			v.add(CodeReplayMismatch, "error", id, "the slot has no slot_added row (record %s)", st.record)
		case !f.broken && !st.unconsumed && !sameTake(f.last, st.expected):
			v.add(CodeReplayMismatch, "error", id, "the changelog ends at %s, the slot resolves to %s", takeStr(f.last), takeStr(st.expected))
		}
	}
	return nil
}

func sameTake(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func takeStr(t *string) string {
	if t == nil {
		return "no take"
	}
	return *t
}

// r6: cover_seed = CoverSeed(id, rerolls); first_published_at is set once a Record was ever published; a published Record
// with no playable slot is a warning.
func (v *verifier) r6() error {
	var published []string
	if err := v.each(`SELECT id, cover_seed, cover_rerolls, status, first_published_at IS NULL,
		EXISTS (SELECT 1 FROM record_changes c WHERE c.record_id = records.id AND c.kind = 'published') FROM records ORDER BY id`, nil, func(r *sql.Rows) error {
		var id, status string
		var seed int64
		var rerolls int
		var noFirst, everPublished bool
		if err := r.Scan(&id, &seed, &rerolls, &status, &noFirst, &everPublished); err != nil {
			return err
		}
		if want := CoverSeed(id, rerolls); seed != want {
			v.add(CodeCoverSeed, "error", id, "cover_seed %d, want %d for %d re-rolls", seed, want, rerolls)
		}
		if noFirst && (everPublished || status == string(StatusPublished)) {
			v.add(CodeFirstPublished, "error", id, "the Record was published but first_published_at is empty")
		}
		if status == string(StatusPublished) {
			published = append(published, id)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, id := range published {
		res, err := resolveRecord(v.ctx, v.q, id)
		if err != nil {
			return err
		}
		playable := 0
		for _, r := range res {
			if r.State == StateOK {
				playable++
			}
		}
		if playable == 0 {
			v.add(CodePublishedEmpty, "warn", id, "published with no playable slot")
		}
	}
	return nil
}

// r7: manual Crate items match the Crate's subject (and are unique per live member, which the unique indexes also hold);
// a smart filter the current vocabulary refuses is a warning.
func (v *verifier) r7() error {
	if err := v.each(`SELECT i.id, i.crate_id, c.subject FROM crate_items i JOIN crates c ON c.id = i.crate_id
		WHERE c.kind <> 'manual' OR (i.song_id IS NOT NULL AND c.subject <> 'song') OR (i.track_id IS NOT NULL AND c.subject <> 'take') ORDER BY i.id`, nil, func(r *sql.Rows) error {
		var id, crate, subject string
		if err := r.Scan(&id, &crate, &subject); err != nil {
			return err
		}
		v.add(CodeCrateItem, "error", id, "item does not match crate %s (subject %s)", crate, subject)
		return nil
	}); err != nil {
		return err
	}
	if err := v.each(`SELECT crate_id, COALESCE(song_id, track_id), COUNT(*) FROM crate_items WHERE removed_at IS NULL
		GROUP BY crate_id, COALESCE(song_id, track_id) HAVING COUNT(*) > 1`, nil, func(r *sql.Rows) error {
		var crate, member string
		var n int
		if err := r.Scan(&crate, &member, &n); err != nil {
			return err
		}
		v.add(CodeCrateItem, "error", crate, "member %s is listed %d times", member, n)
		return nil
	}); err != nil {
		return err
	}
	check := crateFilterCheck.Load()
	if check == nil {
		return nil
	}
	return v.each(`SELECT id, subject, filter_json FROM crates WHERE kind = 'smart' ORDER BY id`, nil, func(r *sql.Rows) error {
		var id, subject, filter string
		if err := r.Scan(&id, &subject, &filter); err != nil {
			return err
		}
		if err := (*check)(subject, json.RawMessage(filter)); err != nil {
			v.add(CodeCrateFilterInvalid, "warn", id, "the stored filter is no longer valid")
		}
		return nil
	})
}

// r8: at most one live tape per key; no running tape whose lease expired more than the TTL ago. (The file check of ready
// tapes is `library verify --files`, lane T2.)
func (v *verifier) r8() error {
	if err := v.each(`SELECT key, COUNT(*) FROM tapes WHERE state IN ('queued','running','ready') GROUP BY key HAVING COUNT(*) > 1`, nil, func(r *sql.Rows) error {
		var key string
		var n int
		if err := r.Scan(&key, &n); err != nil {
			return err
		}
		v.add(CodeTapeKey, "error", key, "%d live tapes share the key", n)
		return nil
	}); err != nil {
		return err
	}
	cutoff := store.FormatTime(v.now.Add(-tapeLeaseTTL))
	return v.each(`SELECT id, lease_expires_at FROM tapes WHERE state = 'running' AND lease_expires_at < ? ORDER BY id`, []any{cutoff}, func(r *sql.Rows) error {
		var id, at string
		if err := r.Scan(&id, &at); err != nil {
			return err
		}
		v.add(CodeTapeLease, "error", id, "the lease expired at %s", at)
		return nil
	})
}

// r9: the settings keys under curation. are exactly the known ones; the two flags hold true or false.
func (v *verifier) r9() error {
	return v.each(`SELECT key, value FROM settings WHERE key LIKE 'curation.%' ORDER BY key`, nil, func(r *sql.Rows) error {
		var key, value string
		if err := r.Scan(&key, &value); err != nil {
			return err
		}
		switch {
		case !slices.Contains(curationKeys, key):
			v.add(CodeSettingKey, "error", key, "unknown curation setting")
		case slices.Contains(curationFlags, key) && value != "true" && value != "false":
			v.add(CodeSettingValue, "error", key, "a flag must be true or false")
		}
		return nil
	})
}
