// Package synth generates synthetic audio and archives for tests, dev and e2e
// (C21). Everything is rendered at run time by ffmpeg, flac and Go; nothing
// from a real archive is used and no audio is committed. It is production code
// (the seed-synthetic subcommand), so it uses mediatool.Tools, never testtools.
package synth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

const (
	sampleRate = 48000
	runTimeout = 120 * time.Second
	maxOut     = 1 << 20
)

// run executes a tool with the synth timeouts.
func run(ctx context.Context, bin string, args ...string) error {
	_, err := mediatool.Run(ctx, bin, args, mediatool.RunOpts{Timeout: runTimeout, MaxStdout: maxOut})
	return err
}

func ffmpegArgs(extra ...string) []string {
	return append([]string{"-nostdin", "-hide_banner", "-v", "error", "-y", "-protocol_whitelist", "file,pipe"}, extra...)
}

func needAbs(p string) error {
	if !filepath.IsAbs(p) {
		return errors.New("synth: path must be absolute")
	}
	return nil
}

// publish moves tmp to dst (same directory, so the rename is atomic) or removes tmp.
func publish(tmp, dst string, err error) error {
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("synth: publishing %s: %w", filepath.Base(dst), err)
	}
	return nil
}
