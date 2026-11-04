package app

import "github.com/nicolasacchi/metamusic/internal/songpr"

// buildM5SongPR constructs the Song PR service (lane M5-S1 owns this file and points in.late.patches.target at it).
func buildM5SongPR(in m5Inputs, s *m5Services) {
	s.SongPR = songpr.New(songpr.Options{})
}
