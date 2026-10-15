package yue2

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
)

func req(t *testing.T, r Request) json.RawMessage {
	t.Helper()
	return json.RawMessage(CanonicalJSON(r))
}

func validRequest(lyrics string) Request {
	return Request{ID: "syn-a-s1101", Style: "pop, 100 BPM", Lyrics: lyrics, Cot: "full", Seed: 1101}
}

func TestEngineYuE2(t *testing.T) {
	t.Parallel()
	var e engine.Engine = Engine{}
	if e.Name() != "yue2" {
		t.Errorf("Name = %q", e.Name())
	}
	caps := e.Caps()
	want := engine.Caps{Text2Song: true, PlanScore: true, RenderFromScore: true, MaxSeconds: 360, SampleRate: 48000}
	if caps != want {
		t.Errorf("Caps = %+v, want %+v", caps, want)
	}

	// a canonical valid request ⇒ no issues
	ok := req(t, validRequest(songLyrics(4, 4)))
	if issues := e.Validate(ok); len(issues) != 0 {
		t.Errorf("valid request: %+v", issues)
	}
	// an extra key (title is app metadata, not part of the engine's request) ⇒ unknown_key
	withTitle := json.RawMessage(strings.Replace(string(ok), `"style"`, `"title":"T","style"`, 1))
	issues := e.Validate(withTitle)
	if len(issues) != 1 || issues[0].Code != "unknown_key" || issues[0].Severity != "error" {
		t.Errorf("title: %+v", issues)
	}
	// warnings are filtered out: only errors come back
	for _, is := range e.Validate(json.RawMessage(`{"id":"x","style":"s","lyrics":"l","cot":"bogus","seed":-1}`)) {
		if is.Severity != "error" {
			t.Errorf("non-error issue returned: %+v", is)
		}
	}
	if len(e.Validate(json.RawMessage(`{"id":"x","style":"s","lyrics":"l","cot":"bogus","seed":-1}`))) == 0 {
		t.Error("an invalid request produced no issues")
	}

	secs := func(r json.RawMessage) int {
		t.Helper()
		n, err := e.ExpectedAudioSeconds(r)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// intro + 4 sections × 4 lines at 100 BPM ⇒ 105.6 ⇒ 106
	if got := secs(req(t, validRequest("[Intro]\n"+songLyrics(4, 4)))); got != 106 {
		t.Errorf("intro + 4×4 lines = %d, want 106", got)
	}
	// a 2-line song is 12.0 s ⇒ clamped to 30
	if got := secs(req(t, validRequest("[Verse]\nla la\nla la\n"))); got != 30 {
		t.Errorf("2-line song = %d, want 30", got)
	}
	// a timed ABC of 64 bars at Q:1/4=92 (2.609 s/bar, 166.96 s) ⇒ 167
	abc := string(abcDoc("M:4/4\nL:1/8\nQ:1/4=92\nK:C", bars(64)))
	r := validRequest(songLyrics(4, 4))
	r.ABC = &abc
	if got := secs(req(t, r)); got != 167 {
		t.Errorf("timed abc = %d, want 167", got)
	}
	// > 360 ⇒ 360
	if got := secs(req(t, validRequest(songLyrics(60, 4)))); got != 360 {
		t.Errorf("long song = %d, want 360", got)
	}
	if _, err := e.ExpectedAudioSeconds(json.RawMessage(`{"id":"x"}`)); err == nil {
		t.Error("unparseable request accepted")
	}
	if _, err := e.ExpectedAudioSeconds(json.RawMessage(`not json`)); err == nil {
		t.Error("non-JSON accepted")
	}
}

// TestArtifacts_RequiredFollowsObserved: C73.4. score.abc and the plan files are optional-but-verified
// while nothing is observed, required once artifacts.observed.json lists them for the cot, and score.abc is
// never expected for cot=off.
func TestArtifacts_RequiredFollowsObserved(t *testing.T) {
	t.Parallel()
	base := []string{"audio.flac", "request.json", "config.json", "result.json", "mm-result.json"}
	full := json.RawMessage(`{"cot":"full"}`)
	off := json.RawMessage(`{"cot":"off"}`)

	req0, opt0 := Engine{}.Artifacts(full)
	if !slices.Equal(req0, base) {
		t.Errorf("required = %v", req0)
	}
	for _, n := range []string{"score.abc", "plan.json", "plan_manifest.json", "abc_tokens.npy", "prefix.npy", "semantic.npy"} {
		if !slices.Contains(opt0, n) {
			t.Errorf("%s must be optional while unobserved", n)
		}
	}
	if slices.Contains(opt0, "latent.npy") || slices.Contains(req0, "latent.npy") {
		t.Error("latent.npy must never be listed")
	}

	e := Engine{Observed: map[string][]string{"full": {"score.abc", "plan.json"}}}
	reqF, optF := e.Artifacts(full)
	if !slices.Contains(reqF, "score.abc") || !slices.Contains(reqF, "plan.json") || slices.Contains(optF, "score.abc") ||
		!slices.Contains(optF, "plan_manifest.json") {
		t.Errorf("observed full: required %v optional %v", reqF, optF)
	}
	// observation is per cot: melody is still unobserved
	reqM, optM := e.Artifacts(json.RawMessage(`{"cot":"melody"}`))
	if slices.Contains(reqM, "score.abc") || !slices.Contains(optM, "score.abc") {
		t.Errorf("melody: required %v optional %v", reqM, optM)
	}
	// cot=off never lists score.abc, even if (wrongly) observed
	for _, eng := range []Engine{{}, {Observed: map[string][]string{"off": {"score.abc"}}}} {
		reqO, optO := eng.Artifacts(off)
		if slices.Contains(reqO, "score.abc") || slices.Contains(optO, "score.abc") {
			t.Errorf("off lists score.abc: %v %v", reqO, optO)
		}
	}
	// a request without a readable cot is treated as full
	if rq, _ := (Engine{}).Artifacts(json.RawMessage(`not json`)); !slices.Equal(rq, base) {
		t.Errorf("fallback required = %v", rq)
	}
}

// TestIssueConversion_Lossless: engine.Issue mirrors yue2.Issue field for field.
func TestIssueConversion_Lossless(t *testing.T) {
	t.Parallel()
	src := reflect.TypeFor[Issue]()
	dst := reflect.TypeFor[engine.Issue]()
	if src.NumField() != dst.NumField() || src.NumField() != 7 {
		t.Fatalf("field counts: yue2.Issue %d, engine.Issue %d", src.NumField(), dst.NumField())
	}
	names := map[string]bool{}
	for i := range src.NumField() {
		names[src.Field(i).Name] = true
	}
	for i := range dst.NumField() {
		if !names[dst.Field(i).Name] {
			t.Errorf("engine.Issue.%s has no yue2.Issue counterpart", dst.Field(i).Name)
		}
	}
	// every field set to a distinct non-zero value survives the conversion
	in := Issue{Code: "c", Field: "f", Severity: "error", Message: "m", Line: 7, Section: "verse 1", Fatal: true}
	got := ToEngineIssue(in)
	want := engine.Issue{Code: "c", Field: "f", Severity: "error", Message: "m", Line: 7, Section: "verse 1", Fatal: true}
	if got != want {
		t.Errorf("ToEngineIssue = %+v, want %+v", got, want)
	}
	// reflection sweep: no field is left at its zero value after conversion of a fully populated issue
	rv := reflect.ValueOf(got)
	for i := range rv.NumField() {
		if rv.Field(i).IsZero() {
			t.Errorf("field %s lost in conversion", dst.Field(i).Name)
		}
	}
}

func TestWeights_ProvisionMatches(t *testing.T) {
	t.Parallel()
	if len(Weights) != 2 {
		t.Fatalf("Weights has %d entries", len(Weights))
	}
	for repo, rev := range map[string]string{WeightsRepo3B: Weights[WeightsRepo3B], WeightsRepoVAE: Weights[WeightsRepoVAE]} {
		if len(rev) != 40 || strings.Trim(rev, "0123456789abcdef") != "" {
			t.Errorf("%s: revision %q is not 40 lowercase hex digits", repo, rev)
		}
	}
	// the audited pins (2026-10-15, yue2 commit 1647252d); a change is a deliberate edit of this test too
	if Weights[WeightsRepo3B] != "8f24312f187763b9854adeec87e437e2b03bbff1" || Weights[WeightsRepoVAE] != "152733a19ad43aa67e367f9b5503ef8075bb5126" {
		t.Errorf("weights pins changed: %v", Weights)
	}
	// the VAE baked into the worker image is the same revision (single source of truth)
	df, err := os.ReadFile("../../../worker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(df), "ARG VAE_REVISION="+Weights[WeightsRepoVAE]+"\n") {
		t.Errorf("worker/Dockerfile VAE_REVISION does not equal Weights[%s]", WeightsRepoVAE)
	}
	if Weights[WeightsRepo3B] == Weights[WeightsRepoVAE] {
		t.Error("the two weights share one revision")
	}
	// provision writes exactly these into the endpoint environment (C83.6)
	env := WeightsEnv()
	if len(env) != 2 || env["MM_YUE2_3B_REVISION"] != Weights["m-a-p/YuE2-3B"] || env["MM_YUE2_VAE_REVISION"] != Weights["m-a-p/YuE2-Vae"] {
		t.Errorf("WeightsEnv = %v", env)
	}
}
