package songview_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
	"github.com/nicolasacchi/metamusic/internal/songview"
)

const featDigest = "0000000000000000000000000000000000000000000000000000000000000000"

func putFeatures(t *testing.T, w *songstest.World, songID, subjectID string, cols string, args ...any) {
	t.Helper()
	q := `INSERT INTO song_features(song_id, subject_kind, subject_id, ` + cols + `, digest, feature_def, computed_at) VALUES(?, 'take', ?, ` +
		strings.Repeat("?,", len(args)) + `?, 'mm-features/1', '2026-10-01T00:00:00.000Z')`
	if _, err := w.St.W.Exec(q, append(append([]any{songID, subjectID}, args...), featDigest)...); err != nil {
		t.Fatal(err)
	}
}

// TestSongView_Features: the block of C142 from a song_features row: fixed-point columns become BPM and 0..1 values, the key gets a
// name, the duration ratio has three decimals, parts that are NULL stay nil, subject.note and pending are carried, and a Song
// without a row has nil features.
func TestSongView_Features(t *testing.T) {
	t.Parallel()
	w := songstest.New(t)
	sc := w.Sc
	r := sc.Recipe("syn_feat_v1", "pop")
	root := sc.Track(r, "Synthetic Feature Take", "", sc.At(0))
	w.Settle()
	song := w.SongOfTrack(root)
	if v := view(t, w, "song", song, 0, 0); v.Features != nil {
		t.Fatalf("no row: features = %+v", v.Features)
	}
	putFeatures(t, w, song, root, `bpm_x100, bpm_basis, bpm_requested_x100, bpm_measured_x100, bpm_octave, energy_x10000, energy_basis, key_num, key_letter, key_basis, duration_s, duration_expected_s`,
		7200, "measured", 7200, 14400, 1, 6250, "measured", 8, "A", "measured", 205.0, 200.0)
	f := view(t, w, "song", song, 0, 0).Features
	if f == nil {
		t.Fatal("features nil with a row")
	}
	if f.Tempo.BPM == nil || *f.Tempo.BPM != 72 || *f.Tempo.Basis != "measured" || *f.Tempo.RequestedBPM != 72 || *f.Tempo.MeasuredBPM != 144 || !f.Tempo.Octave {
		t.Errorf("tempo = %+v", f.Tempo)
	}
	if f.Energy.Value == nil || *f.Energy.Value != 0.625 || *f.Energy.Basis != "measured" {
		t.Errorf("energy = %+v", f.Energy)
	}
	if f.Key.Camelot == nil || *f.Key.Camelot != "8A" || *f.Key.Name != "A minor" || *f.Key.Basis != "measured" {
		t.Errorf("key = %+v", f.Key)
	}
	if f.DurationRatio == nil || *f.DurationRatio != 1.025 {
		t.Errorf("duration_ratio = %v", f.DurationRatio)
	}
	if f.Subject.Kind != "take" || f.Subject.ID != root || f.Subject.Note != nil || f.Pending {
		t.Errorf("subject = %+v pending %v", f.Subject, f.Pending)
	}
	// a queued take sets pending
	sc.Track(r, "Synthetic Dirty Child", root, sc.At(2))
	if f := view(t, w, "song", song, 0, 0).Features; f == nil || !f.Pending {
		t.Errorf("pending = %+v", f)
	}
}

// TestSongView_FeaturesNullParts: a row with only a declared tempo gives nil energy, key and duration ratio; the note is carried.
func TestSongView_FeaturesNullParts(t *testing.T) {
	t.Parallel()
	w := songstest.New(t)
	sc := w.Sc
	r := sc.Recipe("syn_featnull_v1", "pop")
	root := sc.Track(r, "Synthetic Null Take", "", sc.At(0))
	w.Settle()
	song := w.SongOfTrack(root)
	if _, err := w.St.W.Exec(`INSERT INTO song_features(song_id, subject_kind, subject_id, subject_note, bpm_x100, bpm_basis, digest, feature_def, computed_at)
		VALUES(?, 'draft', 'syn-draft', 'all_takes_discarded', 9000, 'declared', ?, 'mm-features/1', '2026-10-01T00:00:00.000Z')`, song, featDigest); err != nil {
		t.Fatal(err)
	}
	f := view(t, w, "song", song, 0, 0).Features
	if f == nil || f.Tempo.BPM == nil || *f.Tempo.BPM != 90 || *f.Tempo.Basis != "declared" || f.Tempo.RequestedBPM != nil || f.Tempo.MeasuredBPM != nil || f.Tempo.Octave {
		t.Fatalf("tempo = %+v", f)
	}
	if f.Energy.Value != nil || f.Energy.Basis != nil || f.Key.Camelot != nil || f.Key.Name != nil || f.Key.Basis != nil || f.DurationRatio != nil {
		t.Errorf("null parts = %+v", f)
	}
	if f.Subject.Kind != "draft" || f.Subject.Note == nil || *f.Subject.Note != "all_takes_discarded" {
		t.Errorf("subject = %+v", f.Subject)
	}
}

// TestSongView_FeaturesKeyNames pins the 24 Camelot names against the committed table of C135.1.
func TestSongView_FeaturesKeyNames(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"8B": "C major", "3B": "Db major", "10B": "D major", "5B": "Eb major", "12B": "E major", "7B": "F major",
		"2B": "F# major", "9B": "G major", "4B": "Ab major", "11B": "A major", "6B": "Bb major", "1B": "B major",
		"5A": "C minor", "12A": "C# minor", "7A": "D minor", "2A": "Eb minor", "9A": "E minor", "4A": "F minor",
		"11A": "F# minor", "6A": "G minor", "1A": "G# minor", "8A": "A minor", "3A": "Bb minor", "10A": "B minor",
	}
	for code, name := range want {
		if got := songview.CamelotNameForTest(code); got != name {
			t.Errorf("%s = %q, want %q", code, got, name)
		}
	}
}
