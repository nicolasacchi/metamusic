package api

import (
	"errors"
	"reflect"
	"testing"
)

func TestSoundSearchFilter(t *testing.T) {
	t.Parallel()
	f, err := soundSearchFilter(SongFilterIn{
		Sound: []string{"genre_family:reggae", "genre_family:dub", "genre_family:reggae", "tempo_band:slow", "vocal:a:b"}, TempoMismatch: optParam[bool]{Value: true, IsSet: true},
		State: []string{"draft", "picked"}, Q: "x", IncludeHidden: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"genre_family": {"reggae", "dub"}, "tempo_band": {"slow"}, "vocal": {"a:b"}}
	if !reflect.DeepEqual(f.Sounds, want) || f.TempoMismatch == nil || !*f.TempoMismatch || len(f.States) != 2 || f.Q != "x" || !f.IncludeHidden {
		t.Errorf("filter = %+v", f)
	}
	for _, bad := range []string{"colour:red", "genre_family", "genre_family:", ":x", ""} {
		_, err := soundSearchFilter(SongFilterIn{Sound: []string{bad}})
		var p *Problem
		ok := errors.As(err, &p)
		if !ok || p.Status != 422 || p.Code != "INVALID_SOUND_FILTER" {
			t.Errorf("sound=%q: %v", bad, err)
		}
	}
	if f, err := soundSearchFilter(SongFilterIn{}); err != nil || f.Sounds != nil {
		t.Errorf("empty: %+v %v", f, err)
	}
}
