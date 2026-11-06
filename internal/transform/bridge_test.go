package transform_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/transform"
	"github.com/nicolasacchi/metamusic/internal/transform/transformtest"
)

// TestBridge_ErrorMapping: every way an attempt can end maps to its stable code (C210.6), the row is failed with cpu_ms
// recorded, no Take and no file is left, and the stored text carries no path and no secret.
func TestBridge_ErrorMapping(t *testing.T) {
	leak := "panic at DATADIR/incoming/x token mmp_abcdefgh12345678"
	cases := []struct {
		name string
		mod  func(r *rig)
		code string
	}{
		{"exit10", func(r *rig) { r.child.ExitCode, r.child.StderrTail = transform.ExitInvalid, leak }, transform.ErrInvalidTransform},
		{"exit11", func(r *rig) { r.child.ExitCode = transform.ExitBudget }, transform.ErrBudgetExceeded},
		{"exit12", func(r *rig) { r.child.ExitCode = transform.ExitNonFinit }, transform.ErrDSPNonFinite},
		{"exit13", func(r *rig) { r.child.ExitCode, r.child.StderrTail = transform.ExitInternal, leak }, transform.ErrTransformFailed},
		{"exit_other", func(r *rig) { r.child.ExitCode = 77 }, transform.ErrTransformFailed},
		{"deadline", func(r *rig) { r.child.Err = transform.ErrChildDeadline }, transform.ErrTimeout},
		{"memory", func(r *rig) { r.child.Err = transform.ErrChildMemory }, transform.ErrMemoryExceeded},
		{"child_error", func(r *rig) { r.child.Err = errors.New("cannot start") }, transform.ErrTransformFailed},
		{"decode", func(r *rig) { r.media.DecodeErr = errors.New(leak) }, transform.ErrTransformFailed},
		{"encode", func(r *rig) { r.media.EncodeErr = errors.New(leak) }, transform.ErrTransformFailed},
		{"ingest_tool", func(r *rig) { r.ingest.Err = &ingest.Error{Code: ingest.CodeToolFailed, Detail: "flac --test"} }, "INGEST_FAILED:TOOL_FAILED"},
		{"ingest_invalid", func(r *rig) { r.ingest.Err = &ingest.Error{Code: ingest.CodeInvalidAudio, Detail: "bad"} }, "INGEST_FAILED:INVALID_AUDIO"},
		{"ingest_freeze", func(r *rig) { r.ingest.Err = &ingest.Error{Code: ingest.CodeFreezeInvariant} }, "INGEST_FAILED:FREEZE_INVARIANT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			tc.mod(r)
			if tc.name == "decode" || tc.name == "encode" {
				r.media.DecodeTime, r.media.EncodeTime = 250*time.Millisecond, 250*time.Millisecond
			}
			id := r.queue(qspec{})
			if !r.runOne() {
				t.Fatal("nothing claimed")
			}
			row := r.row(id)
			if row.State != "failed" || row.ErrCode != tc.code || !row.CPU.Valid || row.Owner != "" || row.PCM != "" {
				t.Fatalf("row = %+v, want failed %s", row, tc.code)
			}
			if strings.Contains(row.ErrText, r.dir) || strings.Contains(row.ErrText, "mmp_abcdefgh") || strings.Contains(row.ErrText, "/incoming/") || len([]rune(row.ErrText)) > 500 {
				t.Errorf("error_text leaks: %q", row.ErrText)
			}
			if n := r.count(`SELECT count(*) FROM tracks WHERE id = ?`, id); n != 0 {
				t.Error("a failed transform produced a Take")
			}
			if r.exists("incoming/transform-"+id) || r.exists("takes/"+id) {
				t.Error("files survived the failure")
			}
			masters, _ := os.ReadDir(filepath.Join(r.dir, "audio", "masters"))
			for _, m := range masters {
				if strings.Contains(m.Name(), "synthetic") {
					t.Errorf("a master was left behind: %s", m.Name())
				}
			}
			if n := r.count(`SELECT count(*) FROM audit_log WHERE op = 'transform_failed' AND error_code = ?`, tc.code); n != 1 {
				t.Errorf("audit rows = %d", n)
			}
		})
	}
}

// TestBridge_ErrorTextNeverHoldsPaths: a stderr tail full of paths and secrets is redacted before it is stored.
func TestBridge_ErrorTextNeverHoldsPaths(t *testing.T) {
	r := newRig(t, false)
	r.child.ExitCode = transform.ExitInternal
	r.child.StderrTail = "goroutine 1: " + r.dir + "/incoming/transform-x/in-0.f32 Authorization: Bearer abcdefghijklmnop\n" + strings.Repeat("x", 900)
	id := r.queue(qspec{})
	r.runOne()
	row := r.row(id)
	if strings.Contains(row.ErrText, r.dir) || strings.Contains(row.ErrText, "abcdefghijklmnop") || strings.Contains(row.ErrText, "\n") {
		t.Errorf("error_text = %q", row.ErrText)
	}
	if len([]rune(row.ErrText)) > 500 || row.ErrText == "" {
		t.Errorf("error_text length %d", len([]rune(row.ErrText)))
	}
}

// TestBridge_DuplicateAudio: identical audio from two different specs is refused by M1's audio identity; the second row fails
// INGEST_FAILED:DUPLICATE_AUDIO naming the existing Take (never a path), with cpu_ms recorded and nothing left behind.
func TestBridge_DuplicateAudio(t *testing.T) {
	r := newRig(t, false)
	r.child.Same = true
	first := r.queue(qspec{tag: "one"})
	second := r.queue(qspec{tag: "two", created: fxNow.Add(time.Second)})
	r.runOne()
	if r.row(first).State != "done" {
		t.Fatalf("first: %+v", r.row(first))
	}
	r.runOne()
	row := r.row(second)
	if row.State != "failed" || row.ErrCode != "INGEST_FAILED:DUPLICATE_AUDIO" || !row.CPU.Valid {
		t.Fatalf("second: %+v", row)
	}
	if !strings.Contains(row.ErrText, first) || strings.Contains(row.ErrText, "/") {
		t.Errorf("error_text = %q, want the existing take id and no path", row.ErrText)
	}
	if r.exists("takes/"+second) || r.exists("incoming/transform-"+second) {
		t.Error("files of the refused attempt survived")
	}
	if n := r.count(`SELECT count(*) FROM tracks WHERE id IN (?, ?)`, first, second); n != 1 {
		t.Errorf("tracks rows = %d", n)
	}
	if r.ingest.Discards() != 1 {
		t.Errorf("discards = %d", r.ingest.Discards())
	}
	ms, _ := os.ReadDir(filepath.Join(r.dir, "audio", "masters"))
	n := 0
	for _, m := range ms {
		if strings.Contains(m.Name(), "synthetic") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d transform masters on disk, want 1", n)
	}
}

// TestBridge_LoudnessUnsafe: the pipeline's own measurement is a bug alarm against target_lufs_effective (D-T1-14.6): a true
// peak above -0.5 dBTP or an integrated loudness off by more than 1.5 LU discards the ingest and fails LOUDNESS_UNSAFE; a
// limiter that engaged for more than 10 % of the time is allowed up to 6 LU below the target.
func TestBridge_LoudnessUnsafe(t *testing.T) {
	cases := []struct {
		name    string
		lufs    float64
		peak    float64
		engaged float64
		unsafe  bool
	}{
		{"on_target", -16, -3, 0, false},
		{"within_1_5", -17.4, -3, 0, false},
		{"peak_hot", -16, -0.4, 0, true},
		{"too_quiet", -17.6, -3, 0, true},
		{"too_loud", -14.4, -3, 0, true},
		{"limited_quiet_ok", -19, -3, 0.5, false},
		{"limited_too_quiet", -23, -3, 0.5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			r.ingest.Loudness = analysis.Loudness{IntegratedLUFS: tc.lufs, TruePeakDBTP: tc.peak}
			r.child.WriteOut = func(dir string, job transformtest.FakeJob) error {
				if err := r.child.WriteDefault(dir, job); err != nil {
					return err
				}
				b, _ := os.ReadFile(filepath.Join(dir, "report.json"))
				var rep map[string]any
				_ = json.Unmarshal(b, &rep)
				rep["safety"].(map[string]any)["limiter"] = map[string]any{"max_reduction_db": 2.0, "engaged_fraction": tc.engaged}
				b, _ = json.Marshal(rep)
				return os.WriteFile(filepath.Join(dir, "report.json"), b, 0o600)
			}
			id := r.queue(qspec{})
			r.runOne()
			row := r.row(id)
			if tc.unsafe {
				if row.State != "failed" || row.ErrCode != transform.ErrLoudnessUnsafe {
					t.Fatalf("row = %+v, want LOUDNESS_UNSAFE", row)
				}
				if r.ingest.Discards() != 1 || r.exists("takes/"+id) {
					t.Error("the unsafe ingest was not discarded")
				}
				if n := r.count(`SELECT count(*) FROM tracks WHERE id = ?`, id); n != 0 {
					t.Error("an unsafe result became a Take")
				}
			} else if row.State != "done" {
				t.Fatalf("row = %+v, want done", row)
			}
		})
	}
}

// TestBridge_InputChanged: masters are frozen, so an input whose recorded hash no longer matches its take is a corruption alarm:
// INPUT_CHANGED, before the child ever runs, and nothing is written.
func TestBridge_InputChanged(t *testing.T) {
	t.Run("hash_mismatch", func(t *testing.T) {
		r := newRig(t, false)
		id := r.queue(qspec{})
		r.exec(`DROP TRIGGER transform_inputs_immutable`) // tamper the way a corrupted database would look
		r.exec(`UPDATE transform_inputs SET master_sha256 = ? WHERE transform_id = ?`, sha("tampered"), id)
		r.runOne()
		row := r.row(id)
		if row.State != "failed" || row.ErrCode != transform.ErrInputChanged {
			t.Fatalf("row = %+v", row)
		}
		if r.child.Runs() != 0 || r.media.Decoded() != 0 {
			t.Error("work started on a changed input")
		}
		if r.exists("takes/"+id) || r.exists("incoming/transform-"+id) {
			t.Error("files were written")
		}
	})
	t.Run("master_missing", func(t *testing.T) {
		r := newRig(t, false)
		id := r.queue(qspec{})
		var p string
		_ = r.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, r.fx["A"]).Scan(&p)
		if err := os.Remove(filepath.Join(r.dir, p)); err != nil {
			t.Fatal(err)
		}
		r.runOne()
		if row := r.row(id); row.State != "failed" || row.ErrCode != transform.ErrInputChanged {
			t.Fatalf("row = %+v", row)
		}
	})
	t.Run("decoded_length", func(t *testing.T) {
		r := newRig(t, false)
		var p string
		_ = r.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, r.fx["A"]).Scan(&p)
		r.media.Sources[filepath.Base(p)] = transformtest.FakeSrc{Frames: 1000, Channels: 2}
		id := r.queue(qspec{})
		r.runOne()
		if row := r.row(id); row.State != "failed" || row.ErrCode != transform.ErrInputChanged {
			t.Fatalf("row = %+v", row)
		}
	})
}

// TestBridge_JobAndArtifacts: the child gets a job.json of the C210.4 shape with decoded stereo float32 inputs, and the Take
// keeps request.json (the spec) and transform.json (the redacted report, stable keys) as artifacts.
func TestBridge_JobAndArtifacts(t *testing.T) {
	ears := &fakeEars{}
	lufs := -14.25
	ears.lufs = &lufs
	r := newRig(t, false, func(o *transform.Options) { o.Ears = ears })
	var job map[string]any
	var inLen int64
	r.child.WriteOut = func(dir string, j transformtest.FakeJob) error {
		b, err := os.ReadFile(filepath.Join(dir, "job.json"))
		if err != nil {
			return err
		}
		_ = json.Unmarshal(b, &job)
		fi, err := os.Stat(filepath.Join(dir, "in-0.f32"))
		if err != nil {
			return err
		}
		inLen = fi.Size()
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			return errors.New("the work dir is not 0700")
		}
		if _, err := os.Stat(filepath.Join(dir, "in-0.raw")); err == nil {
			return errors.New("the raw decode output was left behind")
		}
		return r.child.WriteDefault(dir, j)
	}
	r.child.CPU = 1200 * time.Millisecond
	r.media.DecodeTime, r.media.EncodeTime = 300*time.Millisecond, 400*time.Millisecond
	id := r.queue(qspec{seed: 7})
	r.runOne()
	if r.row(id).State != "done" {
		t.Fatalf("row = %+v", r.row(id))
	}
	if job["v"] != float64(1) || job["kind"] != "glacier" || job["rate"] != float64(48000) || job["seed"] != float64(7) ||
		job["out"] != "out.pcm24" || job["report"] != "report.json" || job["target_lufs"] != float64(-16) || job["ref_lufs"] != -14.25 {
		t.Errorf("job.json = %v", job)
	}
	if u, _ := job["units_max"].(float64); u <= 0 {
		t.Errorf("units_max = %v", job["units_max"])
	}
	ins := job["inputs"].([]any)
	var frames int64
	_ = r.st.R.QueryRow(`SELECT total_samples FROM tracks WHERE id = ?`, r.fx["A"]).Scan(&frames)
	in0 := ins[0].(map[string]any)
	if len(ins) != 1 || in0["file"] != "in-0.f32" || in0["frames"] != float64(frames) || in0["role"] != "source" {
		t.Errorf("job inputs = %v", ins)
	}
	if inLen != frames*8 {
		t.Errorf("in-0.f32 is %d bytes, want %d (interleaved stereo float32)", inLen, frames*8)
	}
	if r.media.LastRate() != 48000 {
		t.Errorf("encode rate %d", r.media.LastRate())
	}

	// artifacts
	raw, err := os.ReadFile(filepath.Join(r.dir, "takes", id, "transform.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), r.dir) {
		t.Error("transform.json holds a path")
	}
	var tj map[string]any
	if err := json.Unmarshal(raw, &tj); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"v", "dsp_version", "toolchain", "kind", "spec_hash", "pcm_sha256", "frames", "rate", "cpu_ms", "units", "inputs", "safety", "warnings", "synth", "voices", "tool_versions", "blind_spots"} {
		if _, ok := tj[k]; !ok {
			t.Errorf("transform.json lacks %q", k)
		}
	}
	if tj["pcm_sha256"] != r.row(id).PCM || tj["dsp_version"] != transform.DSPVersion || tj["kind"] != "glacier" {
		t.Errorf("transform.json = %v", tj)
	}
	if tc := tj["toolchain"].(map[string]any); tc["go_version"] == "" || tc["goarch"] == "" {
		t.Errorf("toolchain = %v", tc)
	}
	if tv := tj["tool_versions"].(map[string]any); tv["ffmpeg"] != "fake-ffmpeg 1" {
		t.Errorf("tool_versions = %v", tv)
	}
	// cpu_ms = child CPU + the wall time of the tool runs
	cpu := r.row(id).CPU
	if cpu.Int64 != 1200+300+400 || tj["cpu_ms"] != float64(1900) {
		t.Errorf("cpu_ms row %v, transform.json %v, want 1900", cpu, tj["cpu_ms"])
	}
	req, err := os.ReadFile(filepath.Join(r.dir, "takes", id, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec transform.Spec
	if err := json.Unmarshal(req, &spec); err != nil || spec.Kind != transform.KindGlacier || spec.Seed != 7 {
		t.Errorf("request.json = %s (%v)", req, err)
	}
	fi, err := os.Stat(filepath.Join(r.dir, "takes", id, "transform.json"))
	if err != nil || fi.Mode().Perm()&0o222 != 0 {
		t.Errorf("artifact mode %v (frozen artifacts are read-only)", fi.Mode())
	}
	// Ears runs after the commit
	if got := ears.calls(); len(got) != 1 || got[0] != id {
		t.Errorf("Ears analysed %v", got)
	}
}

// TestBridge_EarsFailureIsAWarning: Tier A failing after the commit does not undo the Take.
func TestBridge_EarsFailureIsAWarning(t *testing.T) {
	ears := &fakeEars{err: errors.New("tier a broke")}
	r := newRig(t, false, func(o *transform.Options) { o.Ears = ears })
	id := r.queue(qspec{})
	r.runOne()
	if r.row(id).State != "done" || r.count(`SELECT count(*) FROM tracks WHERE id = ?`, id) != 1 {
		t.Fatal("an Ears failure undid the transform")
	}
}

// TestBridge_Progress: child progress reaches the row through the owner predicate, and the phases walk to encoding and
// ingesting.
func TestBridge_Progress(t *testing.T) {
	var r *rig
	var seen []struct {
		phase string
		p     int
	}
	r = newRig(t, false, func(o *transform.Options) {
		prev := o.Changed
		o.Changed = func(id string) {
			prev(id)
			var phase *string
			var p int
			if err := r.st.R.QueryRow(`SELECT phase, progress_permille FROM transforms WHERE id = ?`, id).Scan(&phase, &p); err == nil && phase != nil {
				seen = append(seen, struct {
					phase string
					p     int
				}{*phase, p})
			}
		}
	})
	r.child.Duration = 40 * time.Millisecond
	r.child.Progress = []int{100, 400, 900}
	id := r.queue(qspec{})
	r.runOne()
	if r.row(id).State != "done" || r.row(id).Progress != 1000 {
		t.Fatalf("row = %+v", r.row(id))
	}
	var phases []string
	last := -1
	for _, s := range seen {
		if len(phases) == 0 || phases[len(phases)-1] != s.phase {
			phases = append(phases, s.phase)
		}
		if s.phase == "rendering" {
			if s.p < last {
				t.Errorf("progress went back: %d after %d", s.p, last)
			}
			last = s.p
		}
	}
	want := []string{"preparing", "rendering", "encoding", "ingesting"}
	if strings.Join(phases, ",") != strings.Join(want, ",") {
		t.Errorf("phases %v, want %v", phases, want)
	}
	if last != 900 {
		t.Errorf("last rendering progress %d, want 900", last)
	}
}

// TestBridge_CrashAfterIngestThenRetry: a crash between the freeze and the commit leaves a master and take files with no
// tracks row. The next start sweeps them (the journal names them), and the retry produces exactly one Take.
func TestBridge_CrashAfterIngestThenRetry(t *testing.T) {
	for _, point := range []string{transform.FaultAfterDecode, transform.FaultAfterChild, transform.FaultAfterEncode, transform.FaultAfterIngest} {
		t.Run(point, func(t *testing.T) {
			crash := true
			r := newRig(t, false, func(o *transform.Options) {
				o.Fault = func(p string) error {
					if crash && p == point {
						return context.Canceled
					}
					return nil
				}
			})
			id := r.queue(qspec{})
			r.runOne()
			if row := r.row(id); row.State != "running" {
				t.Fatalf("a crash must leave the row running: %+v", row)
			}
			if point == transform.FaultAfterIngest && !r.exists("takes/"+id) {
				t.Fatal("the crash point after ingest left no take files: the test is vacuous")
			}
			crash = false
			r.clk.add(transform.LeaseTTL + time.Second)
			ctx := context.Background()
			if err := r.svc.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			if err := r.svc.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if r.exists("takes/"+id) || r.exists("incoming/transform-"+id) {
				t.Error("the sweep left the crashed attempt's files")
			}
			ms, _ := os.ReadDir(filepath.Join(r.dir, "audio", "masters"))
			for _, m := range ms {
				if strings.Contains(m.Name(), "synthetic") {
					t.Errorf("an orphan master survived: %s", m.Name())
				}
			}
			if !r.runOne() || r.row(id).State != "done" {
				t.Fatalf("the retry did not finish: %+v", r.row(id))
			}
			if n := r.count(`SELECT count(*) FROM tracks WHERE id = ?`, id); n != 1 {
				t.Errorf("tracks rows = %d", n)
			}
		})
	}
}

// TestBridge_OrphanRemovedBeforeRetry: even without a startup sweep, an attempt removes the orphan of its predecessor (the id is
// fixed, and M1 refuses an existing takes/<id>).
func TestBridge_OrphanRemovedBeforeRetry(t *testing.T) {
	r := newRig(t, false)
	id := r.queue(qspec{})
	r.writeFile("takes/"+id+"/request.json", []byte("stale"))
	if !r.runOne() || r.row(id).State != "done" {
		t.Fatalf("row = %+v", r.row(id))
	}
}

// TestBridge_ExternalSignalRequeues: a child killed by a signal the supervisor did not send is an interruption: the row goes back
// to queued at once (attempt 1) and fails INTERRUPTED after the second one.
func TestBridge_ExternalSignalRequeues(t *testing.T) {
	r := newRig(t, false)
	r.child.Err = transform.ErrChildSignaled
	id := r.queue(qspec{})
	if !r.runOne() {
		t.Fatal("nothing claimed")
	}
	if row := r.row(id); row.State != "queued" || row.Attempts != 1 || row.Owner != "" {
		t.Fatalf("after the first signal: %+v, want queued after 1 attempt", row)
	}
	if !r.runOne() {
		t.Fatal("the retry was not claimed")
	}
	if row := r.row(id); row.State != "failed" || row.ErrCode != transform.ErrInterrupted || !row.CPU.Valid || row.Attempts != 2 {
		t.Fatalf("after the second signal: %+v, want failed INTERRUPTED", row)
	}
}
