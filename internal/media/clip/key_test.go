package clip_test

import (
	"regexp"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
)

func baseSpec() clip.Spec {
	return clip.Spec{
		MasterSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		MasterPath:   "audio/masters/01/0123.flac",
		SampleRate:   44100, Channels: 2,
		W:       clip.Window{StartMS: 1000, EndMS: 31000},
		GainCDB: -737,
	}
}

func TestSpec_Samples(t *testing.T) {
	t.Parallel()
	s, e := baseSpec().Samples()
	if s != 44100 || e != 1367100 {
		t.Errorf("Samples = %d, %d, want 44100, 1367100", s, e)
	}
}

func TestClipKey_Golden(t *testing.T) {
	t.Parallel()
	const tool = "ffmpeg n9.0.1"
	got := clip.Key(baseSpec(), tool)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got) {
		t.Fatalf("key %q is not 64 hex", got)
	}
	// sha256 of the canonical JSON (sorted keys, compact), computed independently with sha256sum
	const golden = "d0256a022e64cc9a567948f527abf802c919cd02b2d69ae35e8cb8984ba718c7"
	if got != golden {
		t.Errorf("key = %s, want %s", got, golden)
	}
	if clip.Key(baseSpec(), tool) != got {
		t.Error("key is not deterministic")
	}
	// changing any field - the tool included - changes the key
	mut := map[string]func(*clip.Spec){
		"sha":      func(s *clip.Spec) { s.MasterSHA256 = "f" + s.MasterSHA256[1:] },
		"rate":     func(s *clip.Spec) { s.SampleRate = 48000 },
		"channels": func(s *clip.Spec) { s.Channels = 1 },
		"start":    func(s *clip.Spec) { s.W.StartMS += 100 },
		"end":      func(s *clip.Spec) { s.W.EndMS += 100 },
		"gain":     func(s *clip.Spec) { s.GainCDB = 0 },
	}
	for name, m := range mut {
		s := baseSpec()
		m(&s)
		if clip.Key(s, tool) == got {
			t.Errorf("changing %s did not change the key", name)
		}
	}
	if clip.Key(baseSpec(), "ffmpeg n9.0.2") == got {
		t.Error("changing the tool did not change the key")
	}
	// the master's path is not an input: the same bytes under another path give the same clip
	s := baseSpec()
	s.MasterPath = "elsewhere.flac"
	if clip.Key(s, tool) != got {
		t.Error("MasterPath must not influence the key")
	}
}
