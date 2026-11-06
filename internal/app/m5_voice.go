package app

import (
	"os"
	"path/filepath"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/voice"
)

// buildM5Voice constructs the voice service (the optional lane M5-V1 owns this file). The service is always built, so stored
// notes keep playing; recording is available once ffmpeg passes the capability probe on first use. wireM5Voice completes it.
func buildM5Voice(_ m5Inputs, s *m5Services) {
	s.Voice = voice.New(voice.Options{})
}

// voiceInputs is what wireM5Voice draws on.
type voiceInputs struct {
	cfg    config.Config
	st     *store.Store
	data   *os.Root
	signer *serve.Signer
	judg   *judgments.Service
	now    func() time.Time
	newID  func() string
}

// wireM5Voice replaces the skeleton with the wired service, points the comment reader at it for signed MP3 links, and returns
// the media resolver for serve.Chain. It runs after the judgments service and the signer exist.
func wireM5Voice(m5 *m5Services, in voiceInputs) serve.Resolver {
	dir, err := filepath.Abs(in.cfg.DataDir)
	if err != nil {
		dir = in.cfg.DataDir
	}
	m5.Voice = voice.New(voice.Options{Store: in.st, Data: in.data, DataDir: dir, FFmpeg: in.cfg.FFmpeg,
		Signer: in.signer, Now: in.now, NewID: in.newID})
	in.judg.UseVoice(m5.Voice)
	return m5.Voice
}
