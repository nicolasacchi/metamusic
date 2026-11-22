package app

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/songpr"
)

// buildM5SongPR constructs the Song PR service and points generation's late PatchSource at it (lane M5-S1 owns this file).
func buildM5SongPR(in m5Inputs, s *m5Services) {
	var scoreABC func(context.Context, string) ([]byte, string, bool, error)
	if in.lib != nil {
		scoreABC = in.lib.ScoreABC
	}
	s.SongPR = songpr.New(songpr.Options{Store: in.st, Now: in.now, ScoreABC: scoreABC})
	in.late.patches.target = s.SongPR
}
