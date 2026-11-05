package transform_test

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// pcm24 makes n frames of interleaved 24-bit stereo: a sine at amp (0..1) whose left and right differ by one LSB pattern, so a
// bit-exact round trip is a real check.
func pcm24(n int, amp float64) ([]byte, []int32) {
	b := make([]byte, 6*n)
	vals := make([]int32, 2*n)
	for i := 0; i < n; i++ {
		for c := 0; c < 2; c++ {
			v := int32(math.Round(amp*math.Sin(2*math.Pi*400*float64(i)/48000)*8388607)) + int32(c)
			vals[2*i+c] = v
			o := i*6 + c*3
			b[o], b[o+1], b[o+2] = byte(v), byte(v>>8), byte(v>>16)
		}
	}
	return b, vals
}

func toolOut(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, err := exec.Command(bin, args...).CombinedOutput() // #nosec G204 -- test tools with literal argv
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestEncode_24Bit: the C210.6(4) encode through the real ffmpeg yields a 24-bit stereo FLAC that passes flac -t and decodes
// back bit-exactly (decode is native-format, D-T1-19).
func TestEncode_24Bit(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	m := transform.ToolMedia{Tools: tools}
	dir := t.TempDir()
	raw, want := pcm24(24000, 0.25)
	pcm := filepath.Join(dir, "out.pcm24")
	if err := os.WriteFile(pcm, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	flacPath := filepath.Join(dir, "out.flac")
	d, err := m.Encode(context.Background(), pcm, flacPath, 48000)
	if err != nil || d <= 0 {
		t.Fatalf("encode: %v (%v)", err, d)
	}
	if got := toolOut(t, tools.Metaflac, "--show-bps", flacPath); got != "24" {
		t.Errorf("bits per sample = %s, want 24", got)
	}
	if got := toolOut(t, tools.Metaflac, "--show-channels", flacPath); got != "2" {
		t.Errorf("channels = %s", got)
	}
	if got := toolOut(t, tools.Metaflac, "--show-sample-rate", flacPath); got != "48000" {
		t.Errorf("sample rate = %s", got)
	}
	if out, err := exec.Command(tools.FLAC, "-t", flacPath).CombinedOutput(); err != nil { // #nosec G204
		t.Errorf("flac -t: %v\n%s", err, out)
	}
	back := filepath.Join(dir, "back.raw")
	if _, err := m.Decode(context.Background(), flacPath, back); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(back)
	if err != nil || len(b) != 4*len(want) {
		t.Fatalf("decoded %d bytes (%v), want %d", len(b), err, 4*len(want))
	}
	for i, v := range want {
		got := math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		if got != float32(float64(v)/8388608) {
			t.Fatalf("sample %d: %v != %v: the round trip is not bit-exact", i, got, float32(float64(v)/8388608))
		}
	}
	vers := m.Versions(context.Background())
	if !strings.Contains(vers.FFmpeg, "ffmpeg") || !strings.Contains(vers.FLAC, "flac") {
		t.Errorf("versions = %+v", vers)
	}
}

// TestAcceptance_T1_TakeShape (media tier, glacier first): real ffmpeg decode and encode, the real M1 ingest pipeline and a
// fake render child give an ordinary Take: origin job in storage with import_rule transform and no job, 24-bit stereo, the
// transform id equal to the take id, a frozen master (mode 0444, 24 bits, flac -t), a ledger edge whose operator is the kind
// and the two artifacts, and the work dir gone.
func TestAcceptance_T1_TakeShape(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	ears := &fakeEars{}
	r := newRig(t, false, func(o *transform.Options) {
		an, err := ffmpeg.New(context.Background(), tools)
		if err != nil {
			t.Fatal(err)
		}
		o.Media = transform.ToolMedia{Tools: tools}
		o.Tools = tools
		o.Pipeline = ingest.New(o.Data, o.DataDir, tools, an, o.Now)
		o.Ears = ears
	})
	r.child.Amp = 0.13 // reads about -16 LUFS on the real loudness measurement of a 400 Hz sine
	// the parent's master is a real 24-bit FLAC of the length its row says
	var master string
	var frames int64
	if err := r.st.R.QueryRow(`SELECT master_path, total_samples FROM tracks WHERE id = ?`, r.fx["A"]).Scan(&master, &frames); err != nil {
		t.Fatal(err)
	}
	raw, _ := pcm24(int(frames), 0.2)
	tmp := filepath.Join(t.TempDir(), "in.pcm24")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(r.dir, master)); err != nil {
		t.Fatal(err)
	}
	if _, err := (transform.ToolMedia{Tools: tools}).Encode(context.Background(), tmp, filepath.Join(r.dir, master), 48000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(r.dir, master), 0o444); err != nil { // masters are frozen
		t.Fatal(err)
	}

	id := r.queue(qspec{})
	if !r.runOne() {
		t.Fatal("nothing claimed")
	}
	row := r.row(id)
	if row.State != "done" {
		t.Fatalf("row = %+v", row)
	}
	var origin, rule, tid, mpath, mp3 string
	var job *string
	var bits, ch, rate int
	err := r.st.R.QueryRow(`SELECT origin, import_rule, job_id, transform_id, bits_per_sample, channels, sample_rate, master_path FROM tracks WHERE id = ?`, id).
		Scan(&origin, &rule, &job, &tid, &bits, &ch, &rate, &mpath)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "job" || rule != "transform" || job != nil || tid != id || bits != 24 || ch != 2 || rate != 48000 {
		t.Errorf("take: origin %s rule %s job %v transform %s bits %d ch %d rate %d", origin, rule, job, tid, bits, ch, rate)
	}
	abs := filepath.Join(r.dir, mpath)
	if fi, err := os.Stat(abs); err != nil || fi.Mode().Perm() != 0o444 {
		t.Errorf("master mode %v (%v), want 0444", fi.Mode(), err)
	}
	if got := toolOut(t, tools.Metaflac, "--show-bps", abs); got != "24" {
		t.Errorf("master bits = %s", got)
	}
	if out, err := exec.Command(tools.FLAC, "-t", abs).CombinedOutput(); err != nil { // #nosec G204
		t.Errorf("flac -t: %v\n%s", err, out)
	}
	tags := toolOut(t, tools.Metaflac, "--export-tags-to=-", abs)
	if !strings.Contains(tags, id) {
		t.Errorf("the take id is not in the tags: %s", tags)
	}
	if n := r.count(`SELECT count(*) FROM mutations WHERE child_track_id = ? AND operator = 'glacier' AND parent_track_id = ? AND transform_id = ?`, id, r.fx["A"], id); n != 1 {
		t.Errorf("ledger edges = %d", n)
	}
	if err := r.st.R.QueryRow(`SELECT path FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, id).Scan(&mp3); err != nil || !r.exists(mp3) {
		t.Errorf("no mp3_320 rendition: %v", err)
	}
	for _, a := range []string{"request.json", "transform.json"} {
		if !r.exists("takes/" + id + "/" + a) {
			t.Errorf("artifact %s missing", a)
		}
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE track_id = ?`, id); n == 0 {
		t.Error("the ingest wrote no loudness analysis")
	}
	if r.exists("incoming/transform-" + id) {
		t.Error("the work dir survived")
	}
	if got := ears.calls(); len(got) != 1 || got[0] != id {
		t.Errorf("Ears: %v", got)
	}
	// the parent is untouched (rule 7)
	if fi, err := os.Stat(filepath.Join(r.dir, master)); err != nil || fi.Mode().Perm() != 0o444 {
		t.Errorf("parent master mode %v", fi.Mode())
	}
}
