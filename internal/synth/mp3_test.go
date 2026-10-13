package synth_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/mp3info"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func TestSynthMP3(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	t.Parallel()
	tone := synth.Tone{LUFS: -20, Seconds: 1}
	cases := []struct {
		name string
		o    synth.MP3Options
		sr   int
		ch   int
		br   int
	}{
		{"default", synth.MP3Options{}, 44100, 2, 128},
		{"mono22k", synth.MP3Options{SampleRate: 22050, Channels: 1, BitrateK: 64}, 22050, 1, 64},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dst := filepath.Join(t.TempDir(), "x.mp3")
			if err := synth.MP3(context.Background(), testtools.Tools(), dst, tone, c.o); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(dst)
			if err != nil {
				t.Fatal(err)
			}
			info, err := mp3info.Parse(bytes.NewReader(b), int64(len(b)))
			if err != nil {
				t.Fatal(err)
			}
			if info.SampleRate != c.sr || info.Channels != c.ch {
				t.Fatalf("info = %+v", info)
			}
			if !bytes.HasPrefix(b, []byte("ID3")) {
				t.Fatalf("default output should start with ID3, got %x", b[:4])
			}
			ents, _ := os.ReadDir(filepath.Dir(dst))
			if len(ents) != 1 {
				t.Fatalf("temp files left: %v", ents)
			}
		})
	}
	t.Run("noid3", func(t *testing.T) {
		t.Parallel()
		dst := filepath.Join(t.TempDir(), "x.mp3")
		if err := synth.MP3(context.Background(), testtools.Tools(), dst, tone, synth.MP3Options{NoID3: true}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(dst)
		if b[0] != 0xFF {
			t.Fatalf("first byte %#x, want 0xFF", b[0])
		}
		if _, err := mp3info.Parse(bytes.NewReader(b), int64(len(b))); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("relative", func(t *testing.T) {
		t.Parallel()
		if err := synth.MP3(context.Background(), testtools.Tools(), "x.mp3", tone, synth.MP3Options{}); err == nil {
			t.Fatal("relative path accepted")
		}
	})
}
