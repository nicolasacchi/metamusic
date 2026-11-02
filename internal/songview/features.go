package songview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// FeaturesBlock is get_song.features (C142): the Song's tempo, energy and key with their bases, read from its song_features row
// by SQL (songview does not import features, C133). Nil while the Song has no subject (no row); Pending repeats View.Pending.
type FeaturesBlock struct {
	Tempo         FeaturesTempo
	Energy        FeaturesEnergy
	Key           FeaturesKey
	DurationRatio *float64
	Subject       FeaturesSubject
	Pending       bool
}

// FeaturesTempo is the position tempo (BPM) with its basis, the requested and measured tempos and the octave flag.
type FeaturesTempo struct {
	BPM                       *float64
	Basis                     *string
	RequestedBPM, MeasuredBPM *float64
	Octave                    bool
}

// FeaturesEnergy is the energy in 0..1 with its basis.
type FeaturesEnergy struct {
	Value *float64
	Basis *string
}

// FeaturesKey is the Camelot code, its name and its basis.
type FeaturesKey struct{ Camelot, Name, Basis *string }

// FeaturesSubject says which take or draft the row was derived from.
type FeaturesSubject struct {
	Kind, ID string
	Note     *string
}

// Camelot spellings, repeated from features (songview may not import it); TestSongView_FeaturesKeyNames pins all 24.
var (
	majorCamelotNum = [12]int{8, 3, 10, 5, 12, 7, 2, 9, 4, 11, 6, 1}
	majorKeyNames   = [12]string{"C", "Db", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"}
	minorKeyNames   = [12]string{"C", "C#", "D", "Eb", "E", "F", "F#", "G", "G#", "A", "Bb", "B"}
)

// camelotName renders a Camelot code such as 8A as "A minor"; an invalid code gives "".
func camelotName(num int, letter string) string {
	for pc, n := range majorCamelotNum {
		switch {
		case n == num && letter == "B":
			return majorKeyNames[pc] + " major"
		case n == num && letter == "A":
			return minorKeyNames[(pc+9)%12] + " minor" // the relative minor sits a minor third below
		}
	}
	return ""
}

func optStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

// scaled is an integer column divided by div as a float; nil for NULL.
func scaled(n sql.NullInt64, div float64) *float64 {
	if !n.Valid {
		return nil
	}
	v := float64(n.Int64) / div
	return &v
}

// featuresBlock reads the Song's row; nil without one.
func (s *Service) featuresBlock(ctx context.Context, songID string, pending bool) (*FeaturesBlock, error) {
	var b FeaturesBlock
	var bpm, req, meas, energy, keyNum sql.NullInt64
	var bpmBasis, energyBasis, keyLetter, keyBasis, note sql.NullString
	var oct int
	var dur, exp sql.NullFloat64
	err := s.st.R.QueryRowContext(ctx, `SELECT subject_kind, subject_id, subject_note, bpm_x100, bpm_basis, bpm_requested_x100, bpm_measured_x100, bpm_octave,
		energy_x10000, energy_basis, key_num, key_letter, key_basis, duration_s, duration_expected_s FROM song_features WHERE song_id = ?`, songID).Scan(
		&b.Subject.Kind, &b.Subject.ID, &note, &bpm, &bpmBasis, &req, &meas, &oct, &energy, &energyBasis, &keyNum, &keyLetter, &keyBasis, &dur, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("songview: features: %w", err)
	}
	b.Subject.Note = optStr(note)
	b.Tempo = FeaturesTempo{BPM: scaled(bpm, 100), Basis: optStr(bpmBasis), RequestedBPM: scaled(req, 100), MeasuredBPM: scaled(meas, 100), Octave: oct == 1}
	b.Energy = FeaturesEnergy{Value: scaled(energy, 10000), Basis: optStr(energyBasis)}
	if keyNum.Valid {
		code := strconv.Itoa(int(keyNum.Int64)) + keyLetter.String
		b.Key = FeaturesKey{Camelot: &code, Basis: optStr(keyBasis)}
		if name := camelotName(int(keyNum.Int64), keyLetter.String); name != "" {
			b.Key.Name = &name
		}
	}
	if dur.Valid && exp.Valid && exp.Float64 > 0 {
		v := math.Floor(dur.Float64/exp.Float64*1000+0.5) / 1000
		b.DurationRatio = &v
	}
	b.Pending = pending
	return &b, nil
}
