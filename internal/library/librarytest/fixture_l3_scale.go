package librarytest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

var l3ScaleFamilies = []string{"pop", "rock", "hip-hop", "electronic", "dance", "reggae", "funk-soul", "r-and-b", "jazz", "blues", "folk", "country",
	"classical", "latin", "world", "metal", "punk", "ambient", "cinematic", "singer-songwriter"}
var l3ScaleVocals = []string{"male", "female", "duet", "choir", "rap", "instrumental"}
var l3ScaleMoods = []string{"upbeat", "chill", "melancholic", "romantic", "dark", "dreamy", "energetic", "nostalgic", "aggressive", "warm"}

// FixtureL3Scale writes rows only (no audio) for the performance tests: FixtureL2Scale's Songs (two takes each, Picks, Muses,
// no Record) plus a literal take_features row for every take and a song_features row for every Song, with deterministic
// synthetic values (measured tempo, key and energy, the five primary Sound values). It needs schema 11. The queue is not
// drained and the Sound tables are not written.
func FixtureL3Scale(tb testing.TB, st *store.Store, now time.Time, songs, muses int) L2Info {
	tb.Helper()
	info := FixtureL2Scale(tb, st, now, songs, muses, 0)
	tx := begin(tb, st)
	ctx := context.Background()
	at := store.FormatTime(now)
	const takeQ = `INSERT INTO take_features(track_id,bpm_x100,bpm_basis,bpm_requested_x100,bpm_measured_x100,bpm_octave,energy_x10000,energy_basis,key_num,key_letter,key_basis,
		duration_s,digest,feature_def,computed_at) VALUES(?,?,'measured',?,?,0,?,'measured',?,?,'measured',200.0,?,'mm-features/1',?)`
	const songQ = `INSERT INTO song_features(song_id,subject_kind,subject_id,bpm_x100,bpm_basis,bpm_requested_x100,bpm_measured_x100,bpm_octave,energy_x10000,energy_basis,
		key_num,key_letter,key_basis,duration_s,family,vocal,era,mood,language,digest,feature_def,computed_at)
		VALUES(?,'take',?,?,'measured',?,?,0,?,'measured',?,?,'measured',200.0,?,?,NULL,?,NULL,?,'mm-features/1',?)`
	for i := 1; i <= songs; i++ {
		song := info.Songs["S"+fmt.Sprint(i)]
		rows, err := tx.QueryContext(ctx, `SELECT id FROM tracks WHERE root_track_id = (SELECT root_track_id FROM songs WHERE id = ?) ORDER BY generation`, song)
		check(tb, err)
		var takes []string
		for rows.Next() {
			var id string
			check(tb, rows.Scan(&id))
			takes = append(takes, id)
		}
		check(tb, rows.Close())
		bpm := 6000 + (i*37)%18000
		energy := (i * 997) % 10001
		num, letter := 1+i%12, []string{"A", "B"}[i%2]
		for _, take := range takes {
			_, err := tx.ExecContext(ctx, takeQ, take, bpm, bpm, bpm, energy, num, letter, Sha("l3-scale/"+take), at)
			check(tb, err)
		}
		_, err = tx.ExecContext(ctx, songQ, song, takes[len(takes)-1], bpm, bpm, bpm, energy, num, letter,
			l3ScaleFamilies[i%len(l3ScaleFamilies)], l3ScaleVocals[i%len(l3ScaleVocals)], l3ScaleMoods[i%len(l3ScaleMoods)], Sha("l3-scale/"+song), at)
		check(tb, err)
	}
	check(tb, tx.Commit())
	return info
}
