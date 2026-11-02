package sequence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// featureRow is the three features of one take_features or song_features row.
type featureRow struct {
	bpm                 sql.NullInt64
	bpmBasis            sql.NullString
	energy              sql.NullInt64
	energyBasis         sql.NullString
	keyNum              sql.NullInt64
	keyLetter, keyBasis sql.NullString
}

func (r featureRow) song(id string) sequencer.Song {
	s := sequencer.Song{ID: id, Energyx10000: sequencer.NoEnergy}
	if r.bpm.Valid {
		s.BPMx100 = int(r.bpm.Int64)
		s.Bases.BPM = r.bpmBasis.String
	}
	if r.energy.Valid {
		s.Energyx10000 = int(r.energy.Int64)
		s.Bases.Energy = r.energyBasis.String
	}
	if r.keyNum.Valid && r.keyLetter.Valid && r.keyLetter.String != "" {
		s.Key = &sequencer.Camelot{Num: int(r.keyNum.Int64), Letter: r.keyLetter.String[0]}
		s.Bases.Key = r.keyBasis.String
	}
	return s
}

// readFeatures reads the rows of table (take_features keyed by track_id, song_features by song_id) for ids, one statement
// per chunk of ids; an id without a row is absent from the map.
func readFeatures(ctx context.Context, q Querier, table, keyCol string, ids []string) (map[string]featureRow, error) {
	out := make(map[string]featureRow, len(ids))
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		//nolint:gosec // G202: table and column are fixed strings of this package; only "?" placeholders are concatenated
		rows, err := q.QueryContext(ctx, `SELECT `+keyCol+`, bpm_x100, bpm_basis, energy_x10000, energy_basis, key_num, key_letter, key_basis
			FROM `+table+` WHERE `+keyCol+` IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("sequence: read %s: %w", table, err)
		}
		for rows.Next() {
			var id string
			var r featureRow
			if err := rows.Scan(&id, &r.bpm, &r.bpmBasis, &r.energy, &r.energyBasis, &r.keyNum, &r.keyLetter, &r.keyBasis); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("sequence: read %s: %w", table, err)
			}
			out[id] = r
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("sequence: read %s: %w", table, err)
		}
	}
	return out, nil
}

// LoadFeatures reads the three features of each ref with one statement per table (chunked at 500 ids): a ref with a Take
// uses that Take's take_features row, a ref without one the Song's song_features row. A ref whose row is missing yields a
// Song with nothing known (tempo 0, energy NoEnergy, no key), which the planner costs as neutral. The Songs come back in the
// order of refs; the map holds each Song's bases by Song id.
func LoadFeatures(ctx context.Context, q Querier, refs []Ref) ([]sequencer.Song, map[string]FeatureBasis, error) {
	var takeIDs, songIDs []string
	for _, r := range refs {
		if r.TakeID != nil {
			takeIDs = append(takeIDs, *r.TakeID)
		} else {
			songIDs = append(songIDs, r.SongID)
		}
	}
	takes, err := readFeatures(ctx, q, "take_features", "track_id", takeIDs)
	if err != nil {
		return nil, nil, err
	}
	songs, err := readFeatures(ctx, q, "song_features", "song_id", songIDs)
	if err != nil {
		return nil, nil, err
	}
	out := make([]sequencer.Song, len(refs))
	bases := make(map[string]FeatureBasis, len(refs))
	for i, r := range refs {
		var row featureRow
		if r.TakeID != nil {
			row = takes[*r.TakeID]
		} else {
			row = songs[r.SongID]
		}
		out[i] = row.song(r.SongID)
		bases[r.SongID] = out[i].Bases
	}
	return out, bases, nil
}
