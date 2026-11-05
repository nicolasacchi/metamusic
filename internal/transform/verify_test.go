package transform_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/transform"
)

func (r *rig) doneTransform(q qspec) string {
	r.t.Helper()
	id := r.queue(q)
	if !r.runOne() || r.row(id).State != "done" {
		r.t.Fatalf("transform not done: %+v", r.row(id))
	}
	return id
}

func (r *rig) tableCounts() string {
	var parts []string
	for _, tb := range []string{"transforms", "tracks", "recipes", "audit_log", "mutations", "transform_inputs"} {
		parts = append(parts, tb+"="+itoa(r.count(`SELECT count(*) FROM `+tb)))
	}
	return strings.Join(parts, " ")
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// TestVerify_MatchWritesNothing: a done transform re-renders from its stored spec and the parents' masters into a scratch dir
// and its pcm_sha256 matches; no row is written and the scratch dir is gone (C210.9).
func TestVerify_MatchWritesNothing(t *testing.T) {
	r := newRig(t, false)
	id := r.doneTransform(qspec{})
	before := r.tableCounts()
	runs := r.child.Runs()
	rep, err := r.svc.Verify(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Match || rep.Inconclusive || rep.Expected != r.row(id).PCM || rep.Got != rep.Expected {
		t.Fatalf("report = %+v", rep)
	}
	if r.child.Runs() != runs+1 {
		t.Errorf("child runs %d -> %d", runs, r.child.Runs())
	}
	if after := r.tableCounts(); after != before {
		t.Errorf("verify wrote rows: %s -> %s", before, after)
	}
	entries, _ := os.ReadDir(filepath.Join(r.dir, "incoming"))
	if len(entries) != 0 {
		t.Errorf("scratch dirs left: %v", entries)
	}
	if r.ingest.Ingests() != 1 {
		t.Errorf("verify ingested (%d ingests)", r.ingest.Ingests())
	}
}

// TestVerify_MismatchIsReported: different PCM from the same spec on the same toolchain is Match=false, not Inconclusive.
func TestVerify_MismatchIsReported(t *testing.T) {
	r := newRig(t, false)
	id := r.doneTransform(qspec{})
	r.child.Same = true // a "regression": the re-render no longer produces the recorded audio
	rep, err := r.svc.Verify(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Match || rep.Inconclusive || rep.Got == "" || rep.Got == rep.Expected {
		t.Fatalf("report = %+v", rep)
	}
}

// TestVerify_InconclusiveOnToolchain (D-T1-3): when any recorded toolchain field differs from the running one the answer is
// Inconclusive, never Match=false, and nothing is re-rendered. Each field is altered in the take's transform.json.
func TestVerify_InconclusiveOnToolchain(t *testing.T) {
	r := newRig(t, false)
	id := r.doneTransform(qspec{})
	orig, err := os.ReadFile(filepath.Join(r.dir, "takes", id, "transform.json"))
	if err != nil {
		t.Fatal(err)
	}
	put := func(b []byte) {
		t.Helper()
		rel := "takes/" + id + "/transform.json"
		_ = r.data.Remove(rel)
		if err := r.data.WriteFile(rel, b, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	alter := func(mut func(doc map[string]any)) []byte {
		var doc map[string]any
		_ = json.Unmarshal(orig, &doc)
		mut(doc)
		b, _ := json.Marshal(doc)
		return b
	}
	tc := func(doc map[string]any) map[string]any { return doc["toolchain"].(map[string]any) }
	cases := []struct {
		field string
		mut   func(map[string]any)
	}{
		{"go_version", func(d map[string]any) { tc(d)["go_version"] = "go0.0.1" }},
		{"goarch", func(d map[string]any) { tc(d)["goarch"] = "riscv64" }},
		{"goamd64", func(d map[string]any) { tc(d)["goamd64"] = "v3" }},
		{"dsp_version", func(d map[string]any) { d["dsp_version"] = "mm-dsp/0" }},
		{"synth", func(d map[string]any) {
			d["synth"] = map[string]any{"name": "fluidsynth", "version": "FluidSynth 2.3.4", "soundfont_sha256": sha("sf")}
		}},
	}
	runs := r.child.Runs()
	for _, c := range cases {
		put(alter(c.mut))
		rep, err := r.svc.Verify(context.Background(), id)
		if err != nil {
			t.Fatalf("%s: %v", c.field, err)
		}
		if !rep.Inconclusive || rep.Match || rep.Got != "" || rep.Expected != r.row(id).PCM {
			t.Errorf("%s: report = %+v", c.field, rep)
		}
		wantInReason := c.field
		if c.field == "synth" {
			wantInReason = "2.3"
		}
		if !strings.Contains(rep.Reason, wantInReason) {
			t.Errorf("%s: reason = %q", c.field, rep.Reason)
		}
	}
	if r.child.Runs() != runs {
		t.Error("an inconclusive verify still rendered")
	}
	// the unaltered record matches the running toolchain
	put(orig)
	if rep, err := r.svc.Verify(context.Background(), id); err != nil || !rep.Match {
		t.Errorf("unaltered: %+v %v", rep, err)
	}
	// and a running toolchain that differs makes the same record inconclusive (a CI or host change)
	other := false
	r2 := newRig(t, false, func(o *transform.Options) {
		o.Toolchain = func() transform.Toolchain {
			t := transform.RunningToolchain()
			if other {
				t.GoVersion += "-other"
			}
			return t
		}
	})
	id2 := r2.doneTransform(qspec{}) // recorded under the real toolchain
	other = true
	if rep, err := r2.svc.Verify(context.Background(), id2); err != nil || !rep.Inconclusive || !strings.Contains(rep.Reason, "go_version") {
		t.Errorf("host toolchain change: %+v %v", rep, err)
	}
}

// TestVerify_Refusals: an unknown id is 404 and a transform that is not done has no result to verify.
func TestVerify_Refusals(t *testing.T) {
	r := newRig(t, false)
	if _, err := r.svc.Verify(context.Background(), "nope"); err == nil {
		t.Error("an unknown id verified")
	}
	id := r.queue(qspec{})
	if _, err := r.svc.Verify(context.Background(), id); err == nil || !strings.Contains(err.Error(), "queued") {
		t.Errorf("a queued transform verified: %v", err)
	}
}

// TestRunningToolchain: the record names the Go version, the architecture and, on amd64, the GOAMD64 level.
func TestRunningToolchain(t *testing.T) {
	tc := transform.RunningToolchain()
	if !strings.HasPrefix(tc.GoVersion, "go") || tc.GOARCH == "" {
		t.Errorf("toolchain = %+v", tc)
	}
	if tc.GOARCH == "amd64" && tc.GOAMD64 == "" {
		t.Errorf("amd64 without a GOAMD64 level: %+v", tc)
	}
}
