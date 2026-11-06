package acceptance_test

// The T1 acceptance rig (T1-B5): the real server (REST, MCP, audit, library) over a library that holds FixtureT1, with the
// real ffmpeg/flac/metaflac, the real M1 ingest pipeline and the real render child.
//
// servertest.Options carries no T1 injection point (app.T1Options is not reachable from it), so the production wiring runs: the
// transform service starts `<os.Executable()> dsp-run --work <dir>`. Here os.Executable is this test binary, so an init() below
// answers that argv by exec'ing a `metamusic` binary the first test builds (the child is cmd/metamusic; a test binary cannot
// link package main). The path is derived from the parent pid, because the supervisor passes the child no environment.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

const t1ChildDirFmt = "/tmp/mm-t1-child-%d"

func init() {
	if len(os.Args) >= 2 && os.Args[1] == "dsp-run" {
		bin := filepath.Join(fmt.Sprintf(t1ChildDirFmt, os.Getppid()), "metamusic")
		argv := append([]string{bin}, os.Args[1:]...)
		err := syscall.Exec(bin, argv, os.Environ()) // #nosec G204 -- a binary this test process built
		fmt.Fprintln(os.Stderr, "t1 test child:", err)
		os.Exit(13)
	}
}

var (
	t1BuildOnce sync.Once
	t1BuildErr  error
)

// t1BuildChild builds cmd/metamusic once per test process into the directory the init() above looks in.
func t1BuildChild(t testing.TB) {
	t.Helper()
	t1BuildOnce.Do(func() {
		// remove the directories of dead test processes first
		if old, _ := filepath.Glob("/tmp/mm-t1-child-*"); len(old) > 0 {
			for _, d := range old {
				pid, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(d), "mm-t1-child-"))
				if err == nil && pid != os.Getpid() {
					if _, serr := os.Stat(fmt.Sprintf("/proc/%d", pid)); serr != nil {
						_ = os.RemoveAll(d)
					}
				}
			}
		}
		dir := fmt.Sprintf(t1ChildDirFmt, os.Getpid())
		if t1BuildErr = os.MkdirAll(dir, 0o700); t1BuildErr != nil {
			return
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "metamusic"), "github.com/nicolasacchi/metamusic/cmd/metamusic") // #nosec G204 -- fixed argv
		if out, err := cmd.CombinedOutput(); err != nil {
			t1BuildErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if t1BuildErr != nil {
		t.Fatal(t1BuildErr)
	}
}

// t1Rig is the rig plus the handles every T1 acceptance test uses.
type t1Rig struct {
	*servertest.Rig
	T     *testing.T
	H     *servertest.Client // the human
	Info  librarytest.T1Info
	Tools mediatool.Tools
	media transform.ToolMedia
	n     int
}

// newT1Rig starts the server with the real tools and FixtureT1's takes, and puts a real 24-bit FLAC of a synthetic musical signal
// in place of the master of each named label (a label list of nil means A and B). The rows keep their sha256: the transform
// service checks the row, not the bytes, and the frozen-master tests compare bytes before and after.
func newT1Rig(t *testing.T, labels ...string) *t1Rig {
	t.Helper()
	return newT1RigWith(t, nil, labels...)
}

// newT1RigWith is newT1Rig with a hook over the server options (canaries, say).
func newT1RigWith(t *testing.T, mod func(*servertest.Options), labels ...string) *t1Rig {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	t1BuildChild(t)
	o := servertest.Options{Backend: "fake", RealTape: true}
	if mod != nil {
		mod(&o)
	}
	o.M2b.Media = true
	o.Fixture, o.M2c, o.M3, o.L1, o.L2, o.L3, o.RealFeatures = true, true, true, true, true, true, true
	r := servertest.New(t, o)
	info := librarytest.ApplyT1(t, r.Store, r.Clock.Now())
	r.Settle(t)
	tr := &t1Rig{Rig: r, T: t, H: r.Human(t), Info: info, Tools: testtools.Tools()}
	tr.media = transform.ToolMedia{Tools: tr.Tools}
	if len(labels) == 0 {
		labels = []string{"A", "B"}
	}
	for i, l := range labels {
		tr.InstallMaster(l, uint64(i+1))
	}
	return tr
}

// t1Signal is a synthetic musical-ish stereo source: pink noise, a tone and a thump on every beat of a 120 BPM grid.
func t1Signal(frames int, seed uint64) (l, r []float64) {
	const rate = 48000
	secs := float64(frames) / rate
	l, r = dsptest.Pink(seed, frames), dsptest.Pink(seed+100, frames)
	for i := range l {
		l[i] *= 0.4
		r[i] *= 0.4
	}
	tone := dsptest.Sine(rate, 220*math.Pow(2, float64(seed%5)/12), 0.15, secs)
	for k := 0; ; k++ {
		i := int(math.Floor((0.1+float64(k)*0.5)*rate + 0.5))
		if i >= frames {
			break
		}
		for j := 0; j < 400 && i+j < frames; j++ {
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

func t1PCM24(l, r []float64) []byte {
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

// MasterPath is the absolute path of a take's master.
func (tr *t1Rig) MasterPath(id string) string {
	tr.T.Helper()
	var p string
	if err := tr.Store.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, id).Scan(&p); err != nil {
		tr.T.Fatal(err)
	}
	return filepath.Join(tr.DataDir, p)
}

// InstallMaster replaces the (absent) fixture master of a labelled Take by a real 24-bit FLAC of the length its row says.
func (tr *t1Rig) InstallMaster(label string, seed uint64) {
	tr.T.Helper()
	id := tr.Info.Tx[label]
	var frames int64
	if err := tr.Store.R.QueryRow(`SELECT total_samples FROM tracks WHERE id = ?`, id).Scan(&frames); err != nil {
		tr.T.Fatal(err)
	}
	l, r := t1Signal(int(frames), seed)
	tr.installFLAC(id, l, r)
	// calibrate to the loudness the row claims, so "level-matched to the parent" is a statement about the file too
	want := obj(obj(tr.H.OK(tr.T, "GET", "/api/tracks/"+id, nil))["analysis"])
	target := obj(want["loudness"])["integrated_lufs"].(float64)
	got := tr.MeasureLUFS(tr.MasterPath(id))
	g := math.Pow(10, (target-got)/20)
	for i := range l {
		l[i] *= g
		r[i] *= g
	}
	tr.installFLAC(id, l, r)
	if d := math.Abs(tr.MeasureLUFS(tr.MasterPath(id)) - target); d > 0.3 {
		tr.T.Fatalf("calibration of %s is off by %.2f LU", label, d)
	}
}

var iRE = regexp.MustCompile(`(?s)Integrated loudness:\s*I:\s*(-?[0-9.]+) LUFS`)

// MeasureLUFS is the integrated loudness of a file by the real ffmpeg (EBU R128).
func (tr *t1Rig) MeasureLUFS(path string) float64 {
	tr.T.Helper()
	out, err := exec.Command(tr.Tools.FFmpeg, "-nostats", "-hide_banner", "-i", path, "-af", "ebur128", "-f", "null", "-").CombinedOutput() // #nosec G204
	if err != nil {
		tr.T.Fatalf("ffmpeg ebur128: %v\n%s", err, out)
	}
	m := iRE.FindSubmatch(out)
	if m == nil {
		tr.T.Fatalf("no integrated loudness in ffmpeg output:\n%s", out)
	}
	v, _ := strconv.ParseFloat(string(m[1]), 64)
	return v
}

func (tr *t1Rig) installFLAC(id string, l, r []float64) {
	tr.T.Helper()
	abs := tr.MasterPath(id)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		tr.T.Fatal(err)
	}
	_ = os.Chmod(abs, 0o644)
	_ = os.Remove(abs)
	tmp := filepath.Join(tr.T.TempDir(), "in.pcm24")
	if err := os.WriteFile(tmp, t1PCM24(l, r), 0o600); err != nil {
		tr.T.Fatal(err)
	}
	if _, err := tr.media.Encode(context.Background(), tmp, abs, 48000); err != nil {
		tr.T.Fatal(err)
	}
	if err := os.Chmod(abs, 0o444); err != nil {
		tr.T.Fatal(err)
	}
}

// FileSHA is the sha256 of a file.
func fileSHA(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// idem returns a fresh Idempotency-Key header pair.
func (tr *t1Rig) idem() []string {
	tr.n++
	return []string{"Idempotency-Key", fmt.Sprintf("t1-b5-%d", tr.n)}
}

// Create posts create_transform as c and returns the HTTP status and the decoded body.
func (tr *t1Rig) Create(c *servertest.Client, body map[string]any) (int, map[string]any) {
	tr.T.Helper()
	resp := c.Do(tr.T, "POST", "/api/transforms", body, tr.idem()...)
	return resp.Status, obj(resp.JSON(tr.T))
}

// CreateOK creates, requires 2xx and returns the view.
func (tr *t1Rig) CreateOK(body map[string]any) map[string]any {
	tr.T.Helper()
	st, v := tr.Create(tr.H, body)
	if st/100 != 2 {
		tr.T.Fatalf("create_transform answered %d: %v", st, v)
	}
	return v
}

// Drain runs the transform runner until the queue is empty and returns how many it ran.
func (tr *t1Rig) Drain() int {
	tr.T.Helper()
	n := 0
	for {
		ok, err := tr.Services.Transform.RunOne(context.Background())
		if err != nil {
			tr.T.Fatalf("runner: %v", err)
		}
		if !ok {
			return n
		}
		n++
	}
}

// Get reads a transform view over REST.
func (tr *t1Rig) Get(id string) map[string]any {
	tr.T.Helper()
	return obj(tr.H.OK(tr.T, "GET", "/api/transforms/"+id, nil))
}

// RunDone creates, drains and requires the transform to be done.
func (tr *t1Rig) RunDone(body map[string]any) map[string]any {
	tr.T.Helper()
	v := tr.CreateOK(body)
	id := v["id"].(string)
	tr.Drain()
	got := tr.Get(id)
	if got["state"] != "done" {
		tr.T.Fatalf("transform %s ended %v: %v", id, got["state"], got["error"])
	}
	return got
}

// Row reads the pcm_sha256, state and error code of a transform row.
func (tr *t1Rig) Row(id string) (state, pcm, code string) {
	tr.T.Helper()
	var p, c *string
	if err := tr.Store.R.QueryRow(`SELECT state, pcm_sha256, error_code FROM transforms WHERE id = ?`, id).Scan(&state, &p, &c); err != nil {
		tr.T.Fatal(err)
	}
	if p != nil {
		pcm = *p
	}
	if c != nil {
		code = *c
	}
	return
}

// Count runs a scalar count query.
func (tr *t1Rig) Count(q string, a ...any) int {
	tr.T.Helper()
	var n int
	if err := tr.Store.R.QueryRow(q, a...).Scan(&n); err != nil {
		tr.T.Fatal(err)
	}
	return n
}

func glacierBody(src string, factor float64, lengthS float64) map[string]any {
	return map[string]any{"kind": "glacier", "glacier": map[string]any{"source_id": src, "factor": factor, "length_s": lengthS, "start_s": 0.0}}
}

func masterBody(src, preset string) map[string]any {
	return map[string]any{"kind": "master", "master": map[string]any{"source_id": src, "preset": preset}}
}

func widenBody(src string) map[string]any {
	return map[string]any{"kind": "widen", "widen": map[string]any{"source_id": src}}
}

// agentClient returns a REST client with a write-scope PAT.
func (tr *t1Rig) writer() *servertest.Client {
	c, _ := tr.Agent(tr.T, auth.ScopeRead, auth.ScopeWrite)
	return c
}

// decodeMaster reads a Take's master as planar float64 (decoded by the real ffmpeg, native rate).
func (tr *t1Rig) decodeMaster(id string) (l, r []float64) {
	tr.T.Helper()
	dst := filepath.Join(tr.T.TempDir(), "x.f32")
	if _, err := tr.media.Decode(context.Background(), tr.MasterPath(id), dst); err != nil {
		tr.T.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		tr.T.Fatal(err)
	}
	n := len(b) / 8
	l, r = make([]float64, n), make([]float64, n)
	for i := 0; i < n; i++ {
		l[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[8*i:])))
		r[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[8*i+4:])))
	}
	return l, r
}
