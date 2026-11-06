package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

const dspRate = 16000

// skipHeavyDsp skips a numerically heavy render in the fast loop under the race detector (about 10x slower for pure float code
// with no goroutines to race); it runs in the non-race tiers and in full runs.
func skipHeavyDsp(t *testing.T) {
	t.Helper()
	if dspRaceEnabled && testing.Short() {
		t.Skip("heavy DSP test: skipped with -short under the race detector")
	}
}

// writeF32 writes interleaved stereo float32 little-endian.
func writeF32(t testing.TB, path string, l, r []float64) {
	t.Helper()
	b := make([]byte, 8*len(l))
	for i := range l {
		binary.LittleEndian.PutUint32(b[8*i:], math.Float32bits(float32(l[i])))
		binary.LittleEndian.PutUint32(b[8*i+4:], math.Float32bits(float32(r[i])))
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func clicks(bpm, first, secs float64) []float64 {
	x := make([]float64, dsptest.Frames(dspRate, secs))
	for k := 0; ; k++ {
		i := int(math.Floor((first+float64(k)*60/bpm)*dspRate + 0.5))
		if i >= len(x) {
			return x
		}
		x[i] = 0.6
	}
}

func music(seed uint64, secs float64) (l, r []float64) {
	n := dsptest.Frames(dspRate, secs)
	l = dsptest.Pink(seed, n)
	r = dsptest.Pink(seed+1, n)
	s := dsptest.Sine(dspRate, 220, 0.2, secs)
	for i := range l {
		l[i] += s[i]
		r[i] += 0.5 * s[i]
	}
	return l, r
}

// jobDir writes job.json and the inputs of a job and returns the work dir.
func jobDir(t testing.TB, doc map[string]any, inputs ...[2][]float64) string {
	t.Helper()
	dir := t.TempDir()
	var ins []map[string]any
	for i, in := range inputs {
		name := fmt.Sprintf("in-%d.f32", i)
		writeF32(t, filepath.Join(dir, name), in[0], in[1])
		ins = append(ins, map[string]any{"role": "source", "file": name, "frames": len(in[0]), "bpm": nil, "beats_per_bar": nil, "key_shift_s": 0, "downbeat_s": nil})
	}
	job := map[string]any{"v": 1, "kind": "glacier", "rate": dspRate, "seed": 7, "params": map[string]any{}, "inputs": ins,
		"units_max": 1 << 40, "target_lufs": -16.0, "ref_lufs": nil, "out": "out.pcm24", "report": "report.json"}
	for k, v := range doc {
		job[k] = v
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

type childResult struct {
	code     int
	stdout   string
	stderr   string
	reportOf map[string]json.RawMessage
}

func runChild(ctx context.Context, t testing.TB, dir string) childResult {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(ctx, []string{"dsp-run", "--work", dir}, func(string) string { return "" }, strings.NewReader(""), &out, &errb)
	r := childResult{code: code, stdout: out.String(), stderr: errb.String()}
	if raw, err := os.ReadFile(filepath.Join(dir, "report.json")); err == nil {
		_ = json.Unmarshal(raw, &r.reportOf)
	}
	return r
}

var glacierParams = map[string]any{"factor": 10, "start_s": 0.0, "length_s": 1.0, "window_s": 0.1, "fade_in_s": 0.2, "fade_out_s": 0.2}

func TestDspRun_Protocol(t *testing.T) {
	t.Parallel()
	skipHeavyDsp(t)
	l, r := music(1, 2)
	dir := jobDir(t, map[string]any{"params": glacierParams}, [2][]float64{l, r})
	res := runChild(context.Background(), t, dir)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	out, err := os.ReadFile(filepath.Join(dir, "out.pcm24"))
	if err != nil {
		t.Fatal(err)
	}
	const frames = 10 * dspRate // length_s 1.0 x factor 10
	if len(out) != 6*frames {
		t.Fatalf("out.pcm24 is %d bytes, want 6 x %d", len(out), frames)
	}
	var keys []string
	for k := range res.reportOf {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"beat_phase", "blind_spots", "frames", "glacier", "pcm_sha256", "safety", "units", "voices", "warnings"}
	if !slices.Equal(keys, want) {
		t.Errorf("report keys %v, want %v", keys, want)
	}
	sum := sha256.Sum256(out)
	if got := strings.Trim(string(res.reportOf["pcm_sha256"]), `"`); got != hex.EncodeToString(sum[:]) {
		t.Errorf("pcm_sha256 %s is not the hash of out.pcm24", got)
	}
	if string(res.reportOf["frames"]) != strconv.Itoa(frames) {
		t.Errorf("frames %s", res.reportOf["frames"])
	}
	var units int64
	_ = json.Unmarshal(res.reportOf["units"], &units)
	if units <= 0 {
		t.Errorf("units %d", units)
	}
	// progress: well-formed lines, monotone, from 0 to 1000
	last := -1
	var n int
	for _, line := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		var pl struct {
			P     int    `json:"p"`
			Phase string `json:"phase"`
		}
		if err := json.Unmarshal([]byte(line), &pl); err != nil || pl.Phase == "" {
			t.Fatalf("progress line %q", line)
		}
		if pl.P <= last {
			t.Errorf("progress %d after %d", pl.P, last)
		}
		last = pl.P
		n++
	}
	if n < 2 || last != 1000 {
		t.Errorf("%d progress lines, last %d", n, last)
	}
	if len(res.stdout) > 64<<10 {
		t.Errorf("progress is %d bytes", len(res.stdout))
	}
	// a rerun in a second directory gives the same hash
	again := runChild(context.Background(), t, jobDir(t, map[string]any{"params": glacierParams}, [2][]float64{l, r}))
	if string(again.reportOf["pcm_sha256"]) != string(res.reportOf["pcm_sha256"]) {
		t.Error("the same job renders different audio")
	}
}

func TestDspRun_Kinds(t *testing.T) {
	t.Parallel()
	skipHeavyDsp(t)
	l, r := music(2, 14)
	pair := func(kind string, params map[string]any, algn string) childResult {
		doc := map[string]any{"kind": kind, "params": params, "alignment": json.RawMessage(algn)}
		a, b := clicks(120, 0.1, 14), clicks(120, 0.1, 14)
		return runChild(context.Background(), t, jobDir(t, doc, [2][]float64{a, a}, [2][]float64{b, b}))
	}
	algn := `{"inputs":[{"bpm":120,"downbeat_s":0.1,"beats_per_bar":4},{"bpm":120,"downbeat_s":0.1,"beats_per_bar":4}]}`
	cases := map[string]childResult{
		"morph":     pair("morph", map[string]any{"bars": 2, "a_at": map[string]any{"bar": 1}, "b_at": map[string]any{"bar": 0}, "semitones_b": 1}, algn),
		"polytempo": pair("polytempo", map[string]any{"converge_bar": 4, "unison_bars": 2}, algn),
		"master":    runChild(context.Background(), t, jobDir(t, map[string]any{"kind": "master", "params": map[string]any{}}, [2][]float64{l, r})),
		"widen":     runChild(context.Background(), t, jobDir(t, map[string]any{"kind": "widen", "params": map[string]any{}}, [2][]float64{l, r})),
	}
	for name, res := range cases {
		if res.code != 0 {
			t.Errorf("%s: exit %d: %s", name, res.code, res.stderr)
			continue
		}
		if name == "morph" || name == "polytempo" {
			var bp []json.RawMessage
			_ = json.Unmarshal(res.reportOf["beat_phase"], &bp)
			if len(bp) != 2 || string(bp[0]) == "null" {
				t.Errorf("%s: beat_phase %s", name, res.reportOf["beat_phase"])
			}
		}
	}
}

func TestDspRun_Exit11_Budget(t *testing.T) {
	t.Parallel()
	l, r := music(3, 2)
	dir := jobDir(t, map[string]any{"params": glacierParams, "units_max": 5000}, [2][]float64{l, r})
	res := runChild(context.Background(), t, dir)
	if res.code != 11 {
		t.Fatalf("exit %d, want 11: %s", res.code, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.pcm24")); err == nil {
		t.Error("a refused render left an output")
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); err == nil {
		t.Error("a refused render left a report")
	}
}

func TestDspRun_Exit10_BadJob(t *testing.T) {
	t.Parallel()
	skipHeavyDsp(t)
	l, r := music(4, 2)
	in := [2][]float64{l, r}
	cases := []struct {
		name  string
		build func(t *testing.T) string
	}{
		{"unknown kind", func(t *testing.T) string { return jobDir(t, map[string]any{"kind": "nope"}, in) }},
		{"bad params", func(t *testing.T) string {
			return jobDir(t, map[string]any{"params": map[string]any{"factor": 1000}}, in)
		}},
		{"frames larger than the file", func(t *testing.T) string {
			dir := jobDir(t, map[string]any{"params": glacierParams}, in)
			patchJob(t, dir, func(j map[string]any) { j["inputs"].([]any)[0].(map[string]any)["frames"] = len(l) + 1 })
			return dir
		}},
		{"frames smaller than the file", func(t *testing.T) string {
			dir := jobDir(t, map[string]any{"params": glacierParams}, in)
			patchJob(t, dir, func(j map[string]any) { j["inputs"].([]any)[0].(map[string]any)["frames"] = len(l) - 1 })
			return dir
		}},
		{"zero frames", func(t *testing.T) string {
			dir := jobDir(t, map[string]any{"params": glacierParams}, in)
			patchJob(t, dir, func(j map[string]any) { j["inputs"].([]any)[0].(map[string]any)["frames"] = 0 })
			return dir
		}},
		{"missing input file", func(t *testing.T) string {
			dir := jobDir(t, map[string]any{"params": glacierParams}, in)
			_ = os.Remove(filepath.Join(dir, "in-0.f32"))
			return dir
		}},
		{"missing job", func(t *testing.T) string { return t.TempDir() }},
		{"not json", func(t *testing.T) string {
			dir := t.TempDir()
			_ = os.WriteFile(filepath.Join(dir, "job.json"), []byte("{nope"), 0o600)
			return dir
		}},
		{"trailing data", func(t *testing.T) string {
			dir := jobDir(t, map[string]any{"params": glacierParams}, in)
			f, _ := os.OpenFile(filepath.Join(dir, "job.json"), os.O_APPEND|os.O_WRONLY, 0)
			_, _ = f.WriteString(`{"x":1}`)
			_ = f.Close()
			return dir
		}},
		{"version", func(t *testing.T) string { return jobDir(t, map[string]any{"v": 2, "params": glacierParams}, in) }},
		{"rate", func(t *testing.T) string { return jobDir(t, map[string]any{"rate": 100, "params": glacierParams}, in) }},
		{"no inputs", func(t *testing.T) string { return jobDir(t, map[string]any{"params": glacierParams}) }},
		{"three inputs", func(t *testing.T) string { return jobDir(t, map[string]any{"params": glacierParams}, in, in, in) }},
		{"output name", func(t *testing.T) string {
			return jobDir(t, map[string]any{"params": glacierParams, "out": "../escape.pcm24"}, in)
		}},
		{"report name", func(t *testing.T) string {
			return jobDir(t, map[string]any{"params": glacierParams, "report": "/tmp/r.json"}, in)
		}},
		{"traversal", func(t *testing.T) string {
			return withInputFile(t, jobDir(t, map[string]any{"params": glacierParams}, in), "../in-0.f32")
		}},
		{"nested", func(t *testing.T) string {
			return withInputFile(t, jobDir(t, map[string]any{"params": glacierParams}, in), "sub/in-0.f32")
		}},
		{"absolute", func(t *testing.T) string {
			return withInputFile(t, jobDir(t, map[string]any{"params": glacierParams}, in), "/etc/passwd")
		}},
		{"other name", func(t *testing.T) string {
			return withInputFile(t, jobDir(t, map[string]any{"params": glacierParams}, in), "job.json")
		}},
		{"negative budget", func(t *testing.T) string {
			return jobDir(t, map[string]any{"params": glacierParams, "units_max": -1}, in)
		}},
		{"symlink out of the dir", func(t *testing.T) string {
			dir := jobDir(t, map[string]any{"params": glacierParams}, in)
			outside := filepath.Join(t.TempDir(), "x.f32")
			writeF32(t, outside, l, r)
			_ = os.Remove(filepath.Join(dir, "in-0.f32"))
			if err := os.Symlink(outside, filepath.Join(dir, "in-0.f32")); err != nil {
				t.Skip("no symlinks")
			}
			return dir
		}},
		{"two inputs one name", func(t *testing.T) string {
			dir := jobDir(t, map[string]any{"params": glacierParams}, in, in)
			patchJob(t, dir, func(j map[string]any) { j["inputs"].([]any)[1].(map[string]any)["file"] = "in-0.f32" })
			return dir
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := c.build(t)
			res := runChild(context.Background(), t, dir)
			if res.code != 10 {
				t.Fatalf("exit %d, want 10: %s", res.code, res.stderr)
			}
			if _, err := os.Stat(filepath.Join(dir, "out.pcm24")); err == nil {
				t.Error("an invalid job left an output")
			}
			if strings.Contains(res.stderr, dir) {
				t.Errorf("stderr names the work dir: %s", res.stderr)
			}
		})
	}
}

func patchJob(t *testing.T, dir string, f func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		t.Fatal(err)
	}
	var j map[string]any
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	f(j)
	raw, _ = json.Marshal(j)
	if err := os.WriteFile(filepath.Join(dir, "job.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func withInputFile(t *testing.T, dir, name string) string {
	t.Helper()
	patchJob(t, dir, func(j map[string]any) { j["inputs"].([]any)[0].(map[string]any)["file"] = name })
	return dir
}

func TestDspRun_Exit12And13(t *testing.T) {
	t.Parallel()
	skipHeavyDsp(t)
	silent := make([]float64, 2*dspRate)
	l, r := music(5, 2)
	// a silent source renders silence: exit 12
	if res := runChild(context.Background(), t, jobDir(t, map[string]any{"params": glacierParams}, [2][]float64{silent, silent})); res.code != 12 {
		t.Errorf("silent: exit %d: %s", res.code, res.stderr)
	}
	// a NaN in the source is a non-finite render: exit 12
	nan := slices.Clone(l)
	for i := range nan {
		nan[i] = math.NaN()
	}
	if res := runChild(context.Background(), t, jobDir(t, map[string]any{"params": glacierParams}, [2][]float64{nan, r})); res.code != 12 {
		t.Errorf("nan: exit %d: %s", res.code, res.stderr)
	}
	// an interrupted child is an internal failure: exit 13, and it leaves nothing for the bridge to trust
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := jobDir(t, map[string]any{"params": glacierParams}, [2][]float64{l, r})
	if res := runChild(ctx, t, dir); res.code != 13 {
		t.Errorf("cancelled: exit %d: %s", res.code, res.stderr)
	}
	// an output that cannot be created: exit 13
	dir = jobDir(t, map[string]any{"params": glacierParams}, [2][]float64{l, r})
	if err := os.Mkdir(filepath.Join(dir, "out.pcm24"), 0o700); err != nil {
		t.Fatal(err)
	}
	if res := runChild(context.Background(), t, dir); res.code != 13 {
		t.Errorf("unwritable output: exit %d: %s", res.code, res.stderr)
	}
}

func TestDspRun_RelativeDirRefused(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"dsp-run", "--work", "relative/dir"}, {"dsp-run", "--work", ""}, {"dsp-run"}, {"dsp-run", "--work", "/tmp", "extra"}, {"dsp-run", "--nope"}} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), args, func(string) string { return "" }, strings.NewReader(""), &out, &errb); code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, errb.String())
		}
		if out.Len() != 0 {
			t.Errorf("%v: wrote to stdout", args)
		}
	}
}

// TestDspRun_Imports: the child knows only the standard library and the dsp tree (D-T1-20 adds analysis, which the beat-phase
// front end uses): neither the file's own imports nor the dependencies of the packages it imports reach anything else of the
// module (no database, config, HTTP server, provider or auth code).
func TestDspRun_Imports(t *testing.T) {
	t.Parallel()
	const mod = "github.com/nicolasacchi/metamusic/"
	allowed := func(p string) bool {
		rest, ok := strings.CutPrefix(p, mod)
		return !ok || strings.HasPrefix(rest, "internal/dsp/") || rest == "internal/dsp" || rest == "internal/analysis"
	}
	f, err := parser.ParseFile(token.NewFileSet(), "cmd_dsprun.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var own []string
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if strings.HasPrefix(p, mod) {
			own = append(own, p)
		}
		if !allowed(p) {
			t.Errorf("cmd_dsprun.go imports %s", p)
		}
		for _, banned := range []string{"net", "net/http", "database/sql", "os/exec", "plugin"} {
			if p == banned {
				t.Errorf("cmd_dsprun.go imports %s", p)
			}
		}
	}
	if len(own) == 0 {
		t.Fatal("cmd_dsprun.go imports nothing of the dsp tree")
	}
	out, err := exec.Command("go", append([]string{"list", "-deps"}, own...)...).Output() // #nosec G204 -- fixed argv, package paths parsed from our own file
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if !allowed(p) {
			t.Errorf("the dsp packages the child imports depend on %s", p)
		}
	}
}

// TestDspRun_ScratchIsCleanedUp: the finishing stage's spool files live inside the work dir under fixed-pattern names and are
// gone when the run ends, whether it succeeded or not; nothing else is left but the files of the protocol.
func TestDspRun_ScratchIsCleanedUp(t *testing.T) {
	t.Parallel()
	skipHeavyDsp(t)
	l, r := music(8, 2)
	names := func(dir string) []string {
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range es {
			out = append(out, e.Name())
		}
		sort.Strings(out)
		return out
	}
	dir := jobDir(t, map[string]any{"params": glacierParams}, [2][]float64{l, r})
	if res := runChild(context.Background(), t, dir); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if got, want := names(dir), []string{"in-0.f32", "job.json", "out.pcm24", "report.json"}; !slices.Equal(got, want) {
		t.Errorf("work dir after a run: %v, want %v", got, want)
	}
	// a failure after the spools exist (a silent source is found in the first pass; a budget in the render) leaves none either
	silent := make([]float64, 2*dspRate)
	dir = jobDir(t, map[string]any{"params": glacierParams}, [2][]float64{silent, silent})
	if res := runChild(context.Background(), t, dir); res.code != 12 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if got, want := names(dir), []string{"in-0.f32", "job.json"}; !slices.Equal(got, want) {
		t.Errorf("work dir after a failed run: %v, want %v", got, want)
	}
	// a scratch name that is already taken is refused (O_EXCL), not overwritten: exit 13
	dir = jobDir(t, map[string]any{"params": glacierParams}, [2][]float64{l, r})
	if err := os.WriteFile(filepath.Join(dir, "spool-1.bin"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := runChild(context.Background(), t, dir); res.code != 13 {
		t.Errorf("taken scratch name: exit %d: %s", res.code, res.stderr)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "spool-1.bin")); string(b) != "x" {
		t.Error("a file in the work dir was overwritten")
	}
}
