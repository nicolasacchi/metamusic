package transform_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// The media tier of T1-D5: the real `metamusic dsp-run` child, started by B3's supervisor, renders inputs that the real ffmpeg
// decoded, and the real M1 pipeline ingests the result. Nothing here is faked but the Ears analysis call and the audio source:
// the inputs are synthetic signals encoded to 24-bit FLAC in place of the fixture masters.

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

// childBinary builds the real binary once per test process (the build cache makes it cheap after the first time).
func childBinary(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-dsprun-bin-")
		if err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(dir, "metamusic")
		cmd := exec.Command("go", "build", "-o", builtBin, "github.com/nicolasacchi/metamusic/cmd/metamusic") // #nosec G204 -- fixed argv
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBin
}

// toPCM24 encodes planar float samples (|x| <= 1) as interleaved 24-bit little-endian stereo.
func toPCM24(l, r []float64) []byte {
	b := make([]byte, 6*len(l))
	for i := range l {
		for c, x := range [2]float64{l[i], r[i]} {
			v := int32(math.Round(math.Max(-1, math.Min(1, x)) * 8388607))
			o := i*6 + c*3
			b[o], b[o+1], b[o+2] = byte(v), byte(v>>8), byte(v>>16)
		}
	}
	return b
}

// d5Signal is a synthetic musical-ish source of the given length: a click track at bpm (the beat), a tone and pink noise.
func d5Signal(frames int, bpm, first float64, seed uint64) (l, r []float64) {
	const rate = 48000
	secs := float64(frames) / rate
	l, r = dsptest.Pink(seed, frames), dsptest.Pink(seed+1, frames)
	tone := dsptest.Sine(rate, 220*math.Pow(2, float64(seed%5)/12), 0.15, secs)
	for k := 0; ; k++ {
		i := int(math.Floor((first+float64(k)*60/bpm)*rate + 0.5))
		if i >= frames {
			break
		}
		for j := 0; j < 400 && i+j < frames; j++ { // a short decaying thump, so the beat has an onset
			v := 0.5 * math.Exp(-float64(j)/60) * math.Sin(2*math.Pi*70*float64(j)/rate)
			l[i+j] += v
			r[i+j] += v
		}
	}
	for i := range l {
		l[i] += tone[i]
		r[i] += 0.7 * tone[i]
	}
	return l, r
}

// installMaster replaces the fixture master of a take by a real 24-bit FLAC of the signal.
func installMaster(t *testing.T, r *rig, tools mediatool.Tools, label string, bpm float64, seed uint64) {
	t.Helper()
	var master string
	var frames int64
	var rate int
	if err := r.st.R.QueryRow(`SELECT master_path, total_samples, sample_rate FROM tracks WHERE id = ?`, r.fx[label]).Scan(&master, &frames, &rate); err != nil {
		t.Fatal(err)
	}
	if rate != 48000 {
		t.Fatalf("fixture take %s is at %d Hz", label, rate)
	}
	l, rr := d5Signal(int(frames), bpm, 0.1, seed)
	tmp := filepath.Join(t.TempDir(), "in.pcm24")
	if err := os.WriteFile(tmp, toPCM24(l, rr), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(r.dir, master)); err != nil {
		t.Fatal(err)
	}
	if _, err := (transform.ToolMedia{Tools: tools}).Encode(context.Background(), tmp, filepath.Join(r.dir, master), 48000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(r.dir, master), 0o444); err != nil {
		t.Fatal(err)
	}
}

// d5Rig is a rig whose child is the real binary and whose media and ingest are the real tools.
func d5Rig(t *testing.T, bin string, tools mediatool.Tools) *rig {
	t.Helper()
	r := newRig(t, false, func(o *transform.Options) {
		an, err := ffmpeg.New(context.Background(), tools)
		if err != nil {
			t.Fatal(err)
		}
		o.Media = transform.ToolMedia{Tools: tools}
		o.Tools = tools
		o.Pipeline = ingest.New(o.Data, o.DataDir, tools, an, o.Now)
		o.Ears = &fakeEars{}
		o.Child = &transform.ExecChild{Self: bin}
	})
	installMaster(t, r, tools, "A", 120, 1)
	installMaster(t, r, tools, "B", 120, 2)
	return r
}

// queueD5 inserts a transform row with the given resolved params and alignment (rig.queue fixes both).
func queueD5(t *testing.T, r *rig, kind transform.Kind, inputs []string, params string, al *transform.Alignment) string {
	t.Helper()
	r.n++
	id := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd50000+r.n)
	roles := []string{"source", "b"}
	if kind == transform.KindMorph || kind == transform.KindPolytempo {
		roles = []string{"a", "b"}
	}
	var ins []transform.InputRef
	for i, l := range inputs {
		var msha string
		if err := r.st.R.QueryRow(`SELECT master_sha256 FROM tracks WHERE id = ?`, r.fx[l]).Scan(&msha); err != nil {
			t.Fatal(err)
		}
		ins = append(ins, transform.InputRef{Role: roles[i], TrackID: r.fx[l], MasterSHA256: msha})
	}
	specHash := sha("d5|" + string(kind) + "|" + params)
	spec := transform.Spec{ID: "dsp-" + string(kind) + "-" + specHash[:20], Seed: 7, V: 1, Kind: kind, DSPVersion: transform.DSPVersion,
		SampleRate: 48000, Inputs: ins, Params: json.RawMessage(params), Alignment: al, TargetLUFS: -16}
	canon, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := r.st.W.Begin()
	if err != nil {
		t.Fatal(err)
	}
	rid, _, err := r.lib.InsertTransformRecipe(context.Background(), tx, library.SpecRecipe{Kind: string(kind), Canonical: canon, Hash: sha(string(canon)),
		RequestID: spec.ID, Seed: 7}, human.Key())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO transforms(id, kind, recipe_id, spec_hash, title, principal_id, principal_kind, principal_name, state, expected_cpu_ms, max_cpu_ms, out_seconds_est, mem_mb_est, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, string(kind), rid, specHash, "Synthetic "+string(kind), human.Key(), "human", human.Name, "queued", 5_000, 120_000, 20.0, 400, store.FormatTime(fxNow)); err != nil {
		t.Fatal(err)
	}
	for i, in := range ins {
		if _, err := tx.Exec(`INSERT INTO transform_inputs(transform_id, ord, role, track_id, master_sha256) VALUES (?,?,?,?,?)`, id, i, in.Role, in.TrackID, in.MasterSHA256); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func d5Align(bpmA, bpmB float64) *transform.Alignment {
	d := 0.1
	return &transform.Alignment{Inputs: []transform.InputAlign{
		{BPM: bpmA, BPMSource: "explicit", DownbeatS: &d, BeatsPerBar: 4},
		{BPM: bpmB, BPMSource: "explicit", DownbeatS: &d, BeatsPerBar: 4}}}
}

type d5Case struct {
	kind   transform.Kind
	inputs []string
	params string
	align  *transform.Alignment
}

var d5Cases = []d5Case{
	{transform.KindGlacier, []string{"A"}, `{"factor":10,"start_s":0,"length_s":3,"window_s":0.5,"fade_in_s":0.5,"fade_out_s":0.5}`, nil},
	{transform.KindMorph, []string{"A", "B"}, `{"bars":2,"beats_per_bar":4,"a_at":{"bar":0},"b_at":{"bar":0},"bpm_a":120,"bpm_b":120,"semitones_b":2,"key_policy":"to_a"}`, d5Align(120, 120)},
	{transform.KindMaster, []string{"A"}, `{"preset":"warm"}`, nil},
}

// TestAcceptance_T1_RealChild (media tier): glacier, morph and master render end to end through the real child. Each ends as an
// ingested 24-bit Take whose pcm_sha256 matches a rerun, both a re-render by Verify in the same library and the same spec in a
// second, fresh library.
func TestAcceptance_T1_RealChild(t *testing.T) {
	if testing.Short() {
		t.Skip("media tier: builds the binary and renders three kinds; runs in make test and CI")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)
	first := d5Rig(t, bin, tools)
	second := d5Rig(t, bin, tools)
	for _, c := range d5Cases {
		t.Run(string(c.kind), func(t *testing.T) {
			id := queueD5(t, first, c.kind, c.inputs, c.params, c.align)
			if !first.runOne() {
				t.Fatal("nothing claimed")
			}
			row := first.row(id)
			if row.State != "done" || !hex64.MatchString(row.PCM) {
				t.Fatalf("row = %+v", row)
			}
			if !row.CPU.Valid || row.CPU.Int64 <= 0 {
				t.Errorf("cpu_ms = %v", row.CPU)
			}
			var bits, ch, rate int
			var mpath string
			if err := first.st.R.QueryRow(`SELECT bits_per_sample, channels, sample_rate, master_path FROM tracks WHERE id = ?`, id).Scan(&bits, &ch, &rate, &mpath); err != nil {
				t.Fatal(err)
			}
			abs := filepath.Join(first.dir, mpath)
			if bits != 24 || ch != 2 || rate != 48000 || toolOut(t, tools.Metaflac, "--show-bps", abs) != "24" {
				t.Errorf("take: %d bits, %d channels, %d Hz", bits, ch, rate)
			}
			if out, err := exec.Command(tools.FLAC, "-t", abs).CombinedOutput(); err != nil { // #nosec G204
				t.Errorf("flac -t: %v\n%s", err, out)
			}
			td, err := first.data.ReadFile("takes/" + id + "/transform.json")
			if err != nil || !strings.Contains(string(td), row.PCM) {
				t.Errorf("transform.json (%v) does not carry pcm_sha256 %s", err, row.PCM)
			}
			// a rerun in the same library: Verify re-renders from the stored spec and the frozen masters
			rep, err := first.svc.Verify(context.Background(), id)
			if err != nil || !rep.Match || rep.Inconclusive {
				t.Errorf("verify: %+v (%v)", rep, err)
			}
			// the same spec in a fresh library renders the same audio
			id2 := queueD5(t, second, c.kind, c.inputs, c.params, c.align)
			if !second.runOne() {
				t.Fatal("nothing claimed in the second library")
			}
			if got := second.row(id2); got.State != "done" || got.PCM != row.PCM {
				t.Errorf("second library: %+v, pcm %s want %s", got, got.PCM, row.PCM)
			}
			if first.exists("incoming/transform-" + id) {
				t.Error("the work dir survived")
			}
		})
	}
}

// --- measurement (informational) ---------------------------------------------------------------------------------------------

// timingMedia and timingIngest record the wall time of the tool steps of the bridge (decode, encode, ingest).
type timingMedia struct {
	transform.Media
	decode, encode time.Duration
}

func (m *timingMedia) Decode(ctx context.Context, src, dst string) (time.Duration, error) {
	d, err := m.Media.Decode(ctx, src, dst)
	m.decode += d
	return d, err
}

func (m *timingMedia) Encode(ctx context.Context, pcm, out string, rate int) (time.Duration, error) {
	d, err := m.Media.Encode(ctx, pcm, out, rate)
	m.encode += d
	return d, err
}

type timingIngest struct {
	transform.Ingester
	took time.Duration
}

func (i *timingIngest) Ingest(ctx context.Context, req ingest.Request) (ingest.Result, error) {
	t0 := time.Now()
	res, err := i.Ingester.Ingest(ctx, req)
	i.took += time.Since(t0)
	return res, err
}

// measured30 are the jobs of the CPU measurement: a 30 s input per kind (two for the pair kinds) at 48 kHz.
var measured30 = []struct {
	kind, params, align string
	pair                bool
}{
	{"glacier", `{"factor":20,"start_s":0,"length_s":30,"window_s":0.5,"fade_in_s":4,"fade_out_s":4}`, ``, false},
	{"shepard", `{"direction":"up","loop_s":8,"start_s":0,"layers":5,"period_s":24,"duration_s":120}`, ``, false},
	{"ensemble", `{"space":"cathedral","wet_db":-6,"dry_db":-12,"tail_s":20}`, ``, false},
	{"master", `{"preset":"warm"}`, ``, false},
	{"widen", `{}`, ``, false},
	{"morph", `{"bars":8,"beats_per_bar":4,"a_at":{"bar":3},"b_at":{"bar":0},"bpm_a":92,"bpm_b":128,"semitones_b":-3}`,
		`{"inputs":[{"bpm":92,"downbeat_s":0.1,"beats_per_bar":4},{"bpm":128,"downbeat_s":0.1,"beats_per_bar":4}]}`, true},
	{"polytempo", `{"beats_per_bar":4,"bpm_a":90,"bpm_b":120,"converge_bar":6,"unison_bars":4,"pan":"split"}`,
		`{"inputs":[{"bpm":90,"downbeat_s":0.1,"beats_per_bar":4},{"bpm":120,"downbeat_s":0.1,"beats_per_bar":4}]}`, true},
}

// TestD5_MeasureCPU prints the raw numbers for the X0 step 5 calibration: the child's CPU (user+system, from the supervisor's
// ProcessState), the work units it metered and the ns per unit, for a 30 s input per kind, then the tool shares of the bridge.
// It runs only with MM_D5_MEASURE=1 and asserts nothing.
func TestD5_MeasureCPU(t *testing.T) {
	if os.Getenv("MM_D5_MEASURE") != "1" {
		t.Skip("set MM_D5_MEASURE=1 to print the CPU measurement")
	}
	bin := childBinary(t)
	const rate, secs = 48000, 30
	n := rate * secs
	var peakKiB int64
	child := &transform.ExecChild{Self: bin, RSSPoll: 20 * time.Millisecond, ReadRSS: func(pid int) (int64, bool) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return 0, false
		}
		for _, line := range strings.Split(string(b), "\n") {
			if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
				var kib int64
				_, _ = fmt.Sscanf(strings.TrimSpace(rest), "%d", &kib)
				peakKiB = max(peakKiB, kib)
				return 0, true // never trips the guard: this is a measurement
			}
		}
		return 0, false
	}}
	for _, c := range measured30 {
		peakKiB = 0
		dir := t.TempDir()
		var ins []map[string]any
		for i, bpm := range []float64{92, 128} {
			if i == 1 && !c.pair {
				break
			}
			if c.kind == "polytempo" {
				bpm = []float64{90, 120}[i]
			}
			l, r := d5Signal(n, bpm, 0.1, uint64(10+i))
			raw := make([]byte, 8*n)
			for j := range l {
				putF32(raw[8*j:], float32(l[j]))
				putF32(raw[8*j+4:], float32(r[j]))
			}
			name := fmt.Sprintf("in-%d.f32", i)
			if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			ins = append(ins, map[string]any{"role": "source", "file": name, "frames": n})
		}
		job := map[string]any{"v": 1, "kind": c.kind, "rate": rate, "seed": 7, "params": json.RawMessage(c.params), "inputs": ins,
			"units_max": int64(1) << 50, "target_lufs": -16.0, "ref_lufs": nil, "out": "out.pcm24", "report": "report.json"}
		if c.align != "" {
			job["alignment"] = json.RawMessage(c.align)
		}
		raw, _ := json.Marshal(job)
		if err := os.WriteFile(filepath.Join(dir, "job.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		res, err := child.Run(context.Background(), transform.ChildJob{Dir: dir, MemMB: 4096, Deadline: 20 * time.Minute}, nil)
		wall := time.Since(t0)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: exit %d, %v, %s", c.kind, res.ExitCode, err, res.StderrTail)
		}
		var rep struct {
			Units  int64 `json:"units"`
			Frames int64 `json:"frames"`
		}
		b, _ := os.ReadFile(filepath.Join(dir, "report.json"))
		_ = json.Unmarshal(b, &rep)
		t.Logf("MEASURE child kind=%s in_s=%d out_s=%.1f cpu_ms=%d wall_ms=%d units=%d ns_per_unit=%.3f peak_rss_mib=%d", c.kind, secs*len(ins),
			float64(rep.Frames)/rate, res.CPU.Milliseconds(), wall.Milliseconds(), rep.Units, float64(res.CPU.Nanoseconds())/float64(rep.Units), peakKiB>>10)
	}
}

func putF32(b []byte, v float32) {
	u := math.Float32bits(v)
	b[0], b[1], b[2], b[3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
}

// TestD5_MeasureTools prints the bridge's tool shares per second of audio (decode of the input, encode of the output, the M1
// ingest) from a real run of each of the three acceptance kinds. It runs only with MM_D5_MEASURE=1 and asserts nothing.
func TestD5_MeasureTools(t *testing.T) {
	if os.Getenv("MM_D5_MEASURE") != "1" {
		t.Skip("set MM_D5_MEASURE=1 to print the tool measurement")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)
	tm := &timingMedia{Media: transform.ToolMedia{Tools: tools}}
	var ti *timingIngest
	r := newRig(t, false, func(o *transform.Options) {
		an, err := ffmpeg.New(context.Background(), tools)
		if err != nil {
			t.Fatal(err)
		}
		tm.Media = transform.ToolMedia{Tools: tools}
		ti = &timingIngest{Ingester: ingest.New(o.Data, o.DataDir, tools, an, o.Now)}
		o.Media, o.Tools, o.Pipeline, o.Ears, o.Child = tm, tools, ti, &fakeEars{}, &transform.ExecChild{Self: bin}
	})
	installMaster(t, r, tools, "A", 120, 1)
	installMaster(t, r, tools, "B", 120, 2)
	for _, c := range d5Cases {
		*tm, *ti = timingMedia{Media: tm.Media}, timingIngest{Ingester: ti.Ingester}
		id := queueD5(t, r, c.kind, c.inputs, c.params, c.align)
		if !r.runOne() {
			t.Fatal("nothing claimed")
		}
		row := r.row(id)
		var inFrames, outFrames int64
		for _, l := range c.inputs {
			var n int64
			_ = r.st.R.QueryRow(`SELECT total_samples FROM tracks WHERE id = ?`, r.fx[l]).Scan(&n)
			inFrames += n
		}
		_ = r.st.R.QueryRow(`SELECT total_samples FROM tracks WHERE id = ?`, id).Scan(&outFrames)
		t.Logf("MEASURE tools kind=%s state=%s in_s=%.1f out_s=%.1f decode_ms=%d encode_ms=%d ingest_ms=%d row_cpu_ms=%d", c.kind, row.State,
			float64(inFrames)/48000, float64(outFrames)/48000, tm.decode.Milliseconds(), tm.encode.Milliseconds(), ti.took.Milliseconds(), row.CPU.Int64)
	}
}
