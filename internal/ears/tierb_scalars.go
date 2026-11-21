package ears

import (
	"context"
	"fmt"
	"strings"
)

// TierBScalar is the listening-check numbers of one take that the metric critics tier_b.cer (direction -1) and
// tier_b.melody_chroma (direction +1) read (C346.2). It is a convenience over the frozen JSON paths $.overall.cer of the
// tier_b_diction row and $.melody_chroma of the tier_b_adherence row; a field is nil when its row is missing (or, for
// MelodyChroma, when the comparison could not be made).
type TierBScalar struct {
	CER          *float64 `json:"cer"`
	WER          *float64 `json:"wer"`
	MelodyChroma *float64 `json:"melody_chroma"`
}

// scalarChunk bounds the track ids of one query.
const scalarChunk = 500

// TierBScalars reads the scalars of the given takes from their current rows (the definition in force, newest row first). A
// take with neither row is absent from the result. internal/ears/tierb stays free of database access: this method reads the
// rows itself.
func (s *Service) TierBScalars(ctx context.Context, trackIDs []string) (map[string]TierBScalar, error) {
	if err := s.wired(); err != nil {
		return nil, err
	}
	out := map[string]TierBScalar{}
	type newest struct{ createdAt, version string }
	for from := 0; from < len(trackIDs); from += scalarChunk {
		ids := trackIDs[from:min(from+scalarChunk, len(trackIDs))]
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		//nolint:gosec // G202: only "?" placeholders are concatenated
		q := `SELECT track_id, analyzer, version, created_at,
			json_extract(metrics_json, '$.overall.cer'), json_extract(metrics_json, '$.overall.wer'), json_extract(metrics_json, '$.melody_chroma')
			FROM analyses WHERE analyzer IN ('` + anTierBDiction + `', '` + anTierBAdherence + `') AND track_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
		rows, err := s.o.Store.R.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("ears: tier B scalars: %w", err)
		}
		seen := map[[2]string]newest{}
		for rows.Next() {
			var id, an, ver, at string
			var cer, wer, chroma *float64
			if err := rows.Scan(&id, &an, &ver, &at, &cer, &wer, &chroma); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("ears: tier B scalars: %w", err)
			}
			if !strings.HasPrefix(ver, defs[an]+" ") {
				continue
			}
			k := [2]string{id, an}
			if cur, ok := seen[k]; ok && (cur.createdAt > at || (cur.createdAt == at && cur.version >= ver)) {
				continue
			}
			seen[k] = newest{at, ver}
			sc := out[id]
			if an == anTierBDiction {
				sc.CER, sc.WER = cer, wer
			} else {
				sc.MelodyChroma = chroma
			}
			out[id] = sc
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
