package synth

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

func TestFluidsynth_Argv(t *testing.T) {
	got := strings.Join(Argv(Request{SMF: "/w/score.mid", Soundfont: "/sf/gm.sf2", Out: "/w/out.wav", Rate: 48000, Gain: 0.5}), "\n") + "\n"
	path := filepath.Join("testdata", "argv.golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("argv (-want +got):\n%s", diff)
	}
	args := Argv(Request{SMF: "/a.mid", Soundfont: "/b.sf2", Out: "/c.wav"})
	joined := " " + strings.Join(args, " ") + " "
	for _, must := range []string{" -q ", " -ni ", " -R 0 ", " -C 0 ", " -O float ", " -T wav ", " -r 48000 ", " -g 0.5 ", " synth.cpu-cores=1 "} {
		if !strings.Contains(joined, must) {
			t.Errorf("defaults: argv lacks %q: %v", must, args)
		}
	}
	if n := len(args); args[n-3] != "/c.wav" || args[n-2] != "/b.sf2" || args[n-1] != "/a.mid" {
		t.Errorf("soundfont then SMF must end the argv: %v", args)
	}
}

// fake writes an executable shell script standing in for fluidsynth.
func fake(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fluidsynth")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFluidsynth_RenderRunsArgvInMinimalEnv(t *testing.T) {
	work := t.TempDir()
	rec := filepath.Join(work, "record.txt")
	bin := fake(t, `if [ "$1" = --version ]; then echo "FluidSynth runtime version 9.9.9"; echo second; exit 0; fi
printf '%s\n' "$@" > `+rec+`
printf '%s|%s|%s|%s\n' "$HOME" "$LC_ALL" "$USER" "$XDG_CONFIG_HOME" > `+rec+`.env
for a; do out=$a; done
while [ $# -gt 0 ]; do if [ "$1" = -F ]; then printf RIFF > "$2"; fi; shift; done`)
	f, err := NewFluidsynth(context.Background(), bin)
	if err != nil || f.Info() != (Info{Name: "fluidsynth", Version: "FluidSynth runtime version 9.9.9"}) {
		t.Fatalf("%+v %v", f, err)
	}
	t.Setenv("USER", "leak")
	t.Setenv("XDG_CONFIG_HOME", "/leak")
	r := Request{SMF: work + "/s.mid", Soundfont: work + "/f.sf2", Out: work + "/o.wav", Timeout: 5 * time.Second}
	info, err := f.Render(context.Background(), r)
	if err != nil || info.Version == "" {
		t.Fatalf("%+v %v", info, err)
	}
	got, _ := os.ReadFile(rec)
	if diff := cmp.Diff(strings.Join(Argv(r), "\n")+"\n", string(got)); diff != "" {
		t.Errorf("argv passed to the tool (-want +got):\n%s", diff)
	}
	env, _ := os.ReadFile(rec + ".env")
	if string(env) != work+"|C||\n" {
		t.Errorf("environment %q: HOME must be the work directory and nothing else may be inherited", env)
	}
}

func TestFluidsynth_RenderErrors(t *testing.T) {
	work := t.TempDir()
	ok := Request{SMF: work + "/s.mid", Soundfont: work + "/f.sf2", Out: work + "/o.wav", Timeout: 5 * time.Second}
	good := fake(t, `[ "$1" = --version ] && echo v1 && exit 0
while [ $# -gt 0 ]; do if [ "$1" = -F ]; then printf RIFF > "$2"; fi; shift; done`)
	f, err := NewFluidsynth(context.Background(), good)
	if err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Request){
		"relative smf":   func(r *Request) { r.SMF = "s.mid" },
		"relative sf":    func(r *Request) { r.Soundfont = "f.sf2" },
		"relative out":   func(r *Request) { r.Out = "o.wav" },
		"low rate":       func(r *Request) { r.Rate = 100 },
		"high rate":      func(r *Request) { r.Rate = 1 << 20 },
		"negative gain":  func(r *Request) { r.Gain = -1 },
		"huge gain":      func(r *Request) { r.Gain = 100 },
		"negative limit": func(r *Request) { r.Timeout = -1 },
	} {
		r := ok
		mod(&r)
		if _, err := f.Render(context.Background(), r); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}

	failing := fake(t, `[ "$1" = --version ] && echo v1 && exit 0
echo "secret detail $2" >&2; exit 3`)
	ff, _ := NewFluidsynth(context.Background(), failing)
	if _, err := ff.Render(context.Background(), ok); err == nil || !strings.Contains(err.Error(), "exit 3") {
		t.Errorf("exit status: %v", err)
	}

	silent := fake(t, `[ "$1" = --version ] && echo v1 && exit 0
exit 0`)
	fs, _ := NewFluidsynth(context.Background(), silent)
	if _, err := fs.Render(context.Background(), ok); !errors.Is(err, ErrNoOutput) {
		t.Errorf("no output file: %v", err)
	}

	slow := fake(t, `[ "$1" = --version ] && echo v1 && exit 0
sleep 30`)
	fl, _ := NewFluidsynth(context.Background(), slow)
	slowReq := ok
	slowReq.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err = fl.Render(context.Background(), slowReq)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Errorf("timeout: %v after %v", err, time.Since(start))
	}
}

func TestFluidsynth_Unavailable(t *testing.T) {
	for _, bin := range []string{"", "/nonexistent/fluidsynth", fake(t, "exit 1")} {
		if _, err := NewFluidsynth(context.Background(), bin); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%q: %v", bin, err)
		}
	}
}

func TestFluidsynth_VersionIsFirstLineBounded(t *testing.T) {
	long := strings.Repeat("é", 100)
	bin := fake(t, `echo `+long+`; echo second`)
	f, err := NewFluidsynth(context.Background(), bin)
	if err != nil || len(f.Info().Version) > 80 || len(f.Info().Version) < 78 || strings.Contains(f.Info().Version, "second") {
		t.Fatalf("%q %v", f.Info().Version, err)
	}
}

func TestTimeoutFor(t *testing.T) {
	for _, tc := range []struct {
		s    float64
		want time.Duration
	}{{0, 10 * time.Second}, {19, 10 * time.Second}, {60, 30 * time.Second}, {1200, 600 * time.Second}} {
		if got := TimeoutFor(tc.s); got != tc.want {
			t.Errorf("TimeoutFor(%v) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

func writeFile(t *testing.T, dir, name string, n int, fill byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, bytes.Repeat([]byte{fill}, n), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSet_Names(t *testing.T) {
	d := t.TempDir()
	a, b := writeFile(t, d, "a.sf2", 10, 'a'), writeFile(t, d, "b.sf2", 20, 'b')
	s := NewSet([]Font{{"orchestra", b}, {"piano", a}, {"orchestra", a}}, nil)
	if diff := cmp.Diff([]string{"orchestra", "piano"}, s.Names()); diff != "" {
		t.Errorf("configured order, duplicates dropped (-want +got):\n%s", diff)
	}
	if _, _, _, ok := s.Lookup("orchestra"); !ok {
		t.Error("orchestra missing")
	}
	if p, _, _, _ := s.Lookup("orchestra"); p != b {
		t.Errorf("a duplicate name must not replace the first entry: %s", p)
	}
	if _, _, _, ok := s.Lookup("nope"); ok {
		t.Error("unknown name found")
	}
	if n := NewSet(nil, nil).Names(); len(n) != 0 {
		t.Errorf("empty set: %v", n)
	}
}

func TestSet_HashesAndSizes(t *testing.T) {
	p := writeFile(t, t.TempDir(), "a.sf2", 3, 'x')
	_, sha, size, ok := NewSet([]Font{{"a", p}}, nil).Lookup("a")
	if !ok || size != 3 || sha != sha256xxx {
		t.Errorf("sha %s size %d ok %v", sha, size, ok)
	}
}

const sha256xxx = "cd2eb0837c9b4c962c22d2ff8b5441b7b45805887f051d39bf133b583baf6860" // sha256 of "xxx"

func TestSet_UnreadableDisablesOnlyThatName(t *testing.T) {
	d := t.TempDir()
	good := writeFile(t, d, "good.sf2", 5, 'g')
	unreadable := writeFile(t, d, "secret-private-name.sf2", 5, 'u')
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	big := writeFile(t, d, "big.sf2", 40, 'b')
	old := maxSoundfontBytes
	maxSoundfontBytes = 32
	t.Cleanup(func() { maxSoundfontBytes = old })
	var logs bytes.Buffer
	s := NewSet([]Font{
		{"missing", filepath.Join(d, "private-missing-name.sf2")},
		{"unreadable", unreadable},
		{"dir", d},
		{"relative", "rel.sf2"},
		{"big", big},
		{"good", good},
	}, slog.New(slog.NewJSONHandler(&logs, nil)))
	if diff := cmp.Diff([]string{"good"}, s.Names()); diff != "" {
		t.Errorf("only the usable name survives (-want +got):\n%s", diff)
	}
	out := logs.String()
	for _, name := range []string{"missing", "dir", "relative", "big"} {
		if !strings.Contains(out, `"name":"`+name+`"`) {
			t.Errorf("no warning for %s: %s", name, out)
		}
	}
	if strings.Contains(out, "private") || strings.Contains(out, d) || strings.Contains(out, "rel.sf2") {
		t.Errorf("a log line carries a path: %s", out)
	}
}
