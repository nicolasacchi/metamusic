package spectro_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

var (
	noiseMu   sync.Mutex
	noiseDir  string
	noiseOnce = map[int]*sync.Once{}
	noiseErr  = map[int]error{}
)

func TestMain(m *testing.M) {
	code := m.Run()
	if noiseDir != "" {
		_ = os.RemoveAll(noiseDir)
	}
	os.Exit(code)
}

func newFFmpeg(t *testing.T) (*ffmpeg.FFmpeg, mediatool.Tools) {
	t.Helper()
	testtools.Require(t, "ffmpeg")
	tools := testtools.Tools()
	f, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	return f, tools
}

func run(tools mediatool.Tools, args ...string) error {
	_, err := mediatool.Run(context.Background(), tools.FFmpeg, append([]string{"-nostdin", "-v", "error", "-y"}, args...),
		mediatool.RunOpts{Timeout: 120 * time.Second, MaxStdout: 1 << 20})
	return err
}

// whiteNoise returns a mono 16-bit 48 kHz white-noise FLAC at -6 dBFS of the
// given length, generated once per package.
func whiteNoise(t *testing.T, tools mediatool.Tools, seconds int) string {
	t.Helper()
	noiseMu.Lock()
	if noiseDir == "" {
		d, err := os.MkdirTemp("", "spectro-noise-")
		if err != nil {
			noiseMu.Unlock()
			t.Fatal(err)
		}
		noiseDir = d
	}
	once := noiseOnce[seconds]
	if once == nil {
		once = new(sync.Once)
		noiseOnce[seconds] = once
	}
	dir := noiseDir
	noiseMu.Unlock()
	p := filepath.Join(dir, "noise"+strconv.Itoa(seconds)+".flac")
	once.Do(func() {
		err := run(tools, "-f", "lavfi", "-i", fmt.Sprintf("anoisesrc=d=%d:c=white:r=48000:a=0.5:seed=7", seconds),
			"-ac", "1", "-sample_fmt", "s16", p)
		noiseMu.Lock()
		noiseErr[seconds] = err
		noiseMu.Unlock()
	})
	noiseMu.Lock()
	err := noiseErr[seconds]
	noiseMu.Unlock()
	if err != nil {
		t.Fatalf("generate noise: %v", err)
	}
	return p
}
