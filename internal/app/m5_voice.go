package app

import "github.com/nicolasacchi/metamusic/internal/voice"

// buildM5Voice constructs the voice service (the optional lane M5-V1 owns this file).
func buildM5Voice(in m5Inputs, s *m5Services) {
	s.Voice = voice.New(voice.Options{})
}
