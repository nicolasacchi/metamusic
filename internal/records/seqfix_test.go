package records_test

import "fmt"

// feat is one synthetic feature triple: tempo in centi-BPM, energy in 1/10000 and a Camelot key.
type feat struct {
	bpm, energy int
	key         string
}

// tSetFeat maps the Songs S1..S6 of the Records fixture onto the six-Song set of the sequencer fixture (t1..t6), so a
// shuffled Record of them plans to the worked orders of C137.6 (arc input J 89 847 130 000, output 55 198 370 000).
var tSetFeat = map[string]feat{
	"S1": {9600, 3000, "8A"}, "S2": {9800, 4500, "8A"}, "S3": {10000, 5500, "9A"},
	"S4": {10400, 7000, "9A"}, "S5": {10800, 9000, "9B"}, "S6": {10200, 4000, "8B"},
}

func keyParts(k string) (int, string) {
	var n int
	var l string
	if _, err := fmt.Sscanf(k, "%d%1s", &n, &l); err != nil {
		panic(err)
	}
	return n, l
}

const digest64 = "0000000000000000000000000000000000000000000000000000000000000000"

// seedTake writes a take_features row.
func (r *rig) seedTake(trackID string, f feat) {
	r.t.Helper()
	n, l := keyParts(f.key)
	r.exec(`DELETE FROM take_features WHERE track_id = ?`, trackID)
	r.exec(`INSERT INTO take_features(track_id, bpm_x100, bpm_basis, energy_x10000, energy_basis, key_num, key_letter, key_basis, digest, feature_def, computed_at)
		VALUES(?,?, 'measured', ?, 'measured', ?, ?, 'measured', ?, 'mm-features/1', '2026-10-15T12:00:00.000Z')`, trackID, f.bpm, f.energy, n, l, digest64)
}

// seedSong writes a song_features row for the Song's subject (a take id for a take subject).
func (r *rig) seedSong(songID, subjectID string, f feat) {
	r.t.Helper()
	n, l := keyParts(f.key)
	r.exec(`DELETE FROM song_features WHERE song_id = ?`, songID)
	r.exec(`INSERT INTO song_features(song_id, subject_kind, subject_id, bpm_x100, bpm_basis, energy_x10000, energy_basis, key_num, key_letter, key_basis, digest, feature_def, computed_at)
		VALUES(?, 'take', ?, ?, 'measured', ?, 'measured', ?, ?, 'measured', ?, 'mm-features/1', '2026-10-15T12:00:00.000Z')`, songID, subjectID, f.bpm, f.energy, n, l, digest64)
}

// pickOf is the Song's current Pick take ("" when none).
func (r *rig) pickOf(songID string) string {
	var t *string
	_ = r.st.R.QueryRow(`SELECT take_id FROM song_picks WHERE song_id = ?`, songID).Scan(&t)
	if t == nil {
		return ""
	}
	return *t
}

// seedTSet gives S1..S6 the T-set features: on each Song's Pick take, and (as a decoy a take-based plan must ignore) the
// reversed features on the Song itself.
func (r *rig) seedTSet() {
	r.t.Helper()
	for label, f := range tSetFeat {
		song, pick := r.song(label), r.pickOf(r.song(label))
		if pick == "" {
			r.t.Fatalf("%s has no Pick take", label)
		}
		r.seedTake(pick, f)
		r.seedSong(song, pick, feat{bpm: 20000 - f.bpm, energy: 10000 - f.energy, key: f.key})
	}
}
