package mcpgen_test

import (
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// seedL3Features gives every Song of the rig synthetic features (a take_features row on its Pick take and a song_features row)
// so the sequencing cases plan on real rows; it is idempotent and deterministic, and the parity rigs do not run the features
// step. The values walk through tempo, energy and key by the Song's position in id order.
func seedL3Features(r *servertest.Rig) {
	rows, err := r.Store.R.Query(`SELECT s.id, COALESCE(p.take_id, '') FROM songs s LEFT JOIN song_picks p ON p.song_id = s.id ORDER BY s.id`)
	if err != nil {
		panic(err)
	}
	type song struct{ id, take string }
	var all []song
	for rows.Next() {
		var s song
		if err := rows.Scan(&s.id, &s.take); err != nil {
			panic(err)
		}
		all = append(all, s)
	}
	_ = rows.Close()
	const digest = "0000000000000000000000000000000000000000000000000000000000000000"
	for i, s := range all {
		bpm, energy, num := 8000+(i*1373)%5000, (i*2713)%10001, 1+(i*5)%12
		letter := []string{"A", "B"}[i%2]
		if s.take != "" {
			if _, err := r.Store.W.Exec(`DELETE FROM take_features WHERE track_id = ?`, s.take); err != nil {
				panic(err)
			}
			if _, err := r.Store.W.Exec(`INSERT INTO take_features(track_id, bpm_x100, bpm_basis, energy_x10000, energy_basis, key_num, key_letter, key_basis, digest, feature_def, computed_at)
				VALUES(?,?, 'measured', ?, 'measured', ?, ?, 'measured', ?, 'mm-features/1', '2026-10-15T12:00:00.000Z')`, s.take, bpm, energy, num, letter, digest); err != nil {
				panic(err)
			}
		}
		subject, kind := s.take, "take"
		if subject == "" {
			subject, kind = fmt.Sprintf("draft-%d", i), "draft"
		}
		if _, err := r.Store.W.Exec(`DELETE FROM song_features WHERE song_id = ?`, s.id); err != nil {
			panic(err)
		}
		if _, err := r.Store.W.Exec(`INSERT INTO song_features(song_id, subject_kind, subject_id, bpm_x100, bpm_basis, energy_x10000, energy_basis, key_num, key_letter, key_basis, digest, feature_def, computed_at)
			VALUES(?,?,?,?, 'measured', ?, 'measured', ?, ?, 'measured', ?, 'mm-features/1', '2026-10-15T12:00:00.000Z')`, s.id, kind, subject, bpm, energy, num, letter, digest); err != nil {
			panic(err)
		}
	}
}

// l3SeqCases are the parity cases of suggest_sequence and of get_record include=sequence (lane SR owns this file). The L2 rows
// edit_record_slots and propose_record carry the sequence arguments in parity_l2_test.go; the get_record case below runs after
// them and reads the run they stored.
var l3SeqCases = []ParityCase{
	{Op: "suggest_sequence", Variant: "songs", Args: func(r *servertest.Rig, _ map[string]any) map[string]any {
		seedL3Features(r)
		return map[string]any{"song_id": l2Songs(r, 4), "strategy": "ramp_up"}
	}},
	{Op: "suggest_sequence", Variant: "record", Args: func(r *servertest.Rig, _ map[string]any) map[string]any {
		seedL3Features(r)
		return map[string]any{"record_id": oneID(r, `SELECT id FROM records WHERE title = 'Synthetic Record Two'`)}
	}},
	{Op: "suggest_sequence", Variant: "unknown strategy", Status: 422, Args: func(r *servertest.Rig, _ map[string]any) map[string]any {
		return map[string]any{"song_id": l2Songs(r, 2), "strategy": "zigzag"}
	}},
}

// l3SeqRecordCases reads the run the L2 rows stored through get_record include=sequence. It is not one of the six L3 operations
// (TestParity_L3Cases), so it is registered here, after parity_l2_test.go's cases (init order follows the file names).
var l3SeqRecordCases = []ParityCase{
	{Op: "get_record", Variant: "sequence", Masks: l2RecordMasks, Args: func(r *servertest.Rig, _ map[string]any) map[string]any {
		return map[string]any{"id": oneID(r, `SELECT id FROM records WHERE title = 'Synthetic Record 002'`), "include": []string{"sequence", "changes"}}
	}},
}

func init() { parityCases = append(parityCases, l3SeqRecordCases...) }
