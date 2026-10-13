package ffmpeg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// listing renders an ffmpeg -filters / -encoders style listing for names.
func listing(names ...string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString(" T.. " + n + "  A->A  synthetic\n")
	}
	return b.String()
}

var (
	clipFilters  = []string{"ebur128", "atrim", "asetpts", "afade", "aresample", "volume", "ametadata"}
	tierAFilters = []string{"ebur128", "asetnsamples", "astats", "silencedetect", "ametadata", "aformat", "showspectrumpic"}
)

func without(list []string, drop string) []string {
	var out []string
	for _, n := range list {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range append(append([]string{}, a...), b...) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func newWith(t *testing.T, filters, encoders []string) *ffmpeg.FFmpeg {
	t.Helper()
	bin, _ := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", listing(filters...), listing(encoders...), "exit 0")
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

// The tests below write and exec fake ffmpeg scripts, so they stay serial: a parallel fork can inherit the
// script's write descriptor and make the exec fail with "text file busy".
func TestFFmpeg_RequireCapabilities(t *testing.T) {
	all := union(clipFilters, tierAFilters)
	allEnc := []string{"libmp3lame", "flac", "png"}
	tests := []struct {
		name                 string
		filters, encoders    []string
		wantClips, wantTierA string // substring of the error, "" => nil
	}{
		{"M1 capabilities only", []string{"ebur128"}, []string{"libmp3lame"}, "atrim", "asetnsamples"},
		{"everything", all, allEnc, "", ""},
		{"only asetpts missing", without(all, "asetpts"), allEnc, "asetpts", ""},
		{"only showspectrumpic missing", without(all, "showspectrumpic"), allEnc, "", "showspectrumpic"},
		{"flac encoder missing", all, []string{"libmp3lame", "png"}, "flac", ""},
		{"png encoder missing", all, []string{"libmp3lame", "flac"}, "", "png"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newWith(t, tc.filters, tc.encoders)
			for _, c := range []struct {
				what string
				err  error
				want string
			}{{"RequireClips", f.RequireClips(), tc.wantClips}, {"RequireTierA", f.RequireTierA(), tc.wantTierA}} {
				if c.want == "" {
					if c.err != nil {
						t.Errorf("%s = %v, want nil", c.what, c.err)
					}
					continue
				}
				if !errors.Is(c.err, analysis.ErrToolMissing) || !strings.Contains(c.err.Error(), c.want) {
					t.Errorf("%s = %v, want ErrToolMissing naming %q", c.what, c.err, c.want)
				}
			}
		})
	}
}

// A longer filter name must not satisfy a capability (whole-word match).
func TestFFmpeg_RequireCapabilitiesWholeWord(t *testing.T) {
	f := newWith(t, []string{"ebur128", "xatrimy", "asetpts", "afade", "aresample", "volume", "ametadata"}, []string{"libmp3lame", "flac"})
	if err := f.RequireClips(); err == nil || !strings.Contains(err.Error(), "atrim") {
		t.Fatalf("RequireClips = %v, want atrim missing", err)
	}
}
