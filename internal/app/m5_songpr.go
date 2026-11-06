package app

import "github.com/nicolasacchi/metamusic/internal/songpr"

// buildM5SongPR constructs the Song PR service and points generation's late PatchSource at it (lane M5-S1 owns this file).
func buildM5SongPR(in m5Inputs, s *m5Services) {
	s.SongPR = songpr.New(songpr.Options{Store: in.st, Now: in.now})
	in.late.patches.target = s.SongPR
}
