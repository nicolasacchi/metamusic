package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Submit, input minting and preflight for non-yue2 engines (C153.3, C156). Fixtures are synthetic.

type clipResolver map[string]serve.Object

func (c clipResolver) Resolve(_ context.Context, sha, ext string) (serve.Object, error) {
	if o, ok := c[sha+"."+ext]; ok {
		return o, nil
	}
	return serve.Object{}, serve.ErrNotFound
}

// masterOf inserts a synthetic track through the stub ingester and returns its id, master sha and size.
func masterOf(t *testing.T, r *rig) (id, sha string, bytes int64) {
	t.Helper()
	id, err := r.ing.IngestTake(t.Context(), JobRow{RecipeID: testRecipe}, AttemptRow{}, nil, provider.Output{})
	if err != nil {
		t.Fatal(err)
	}
	sha = r.str(`SELECT master_sha256 FROM tracks WHERE id = ?`, id)
	return id, sha, int64(r.count(`SELECT master_bytes FROM tracks WHERE id = ?`, id))
}

func minterRig(t *testing.T) (*rig, *serve.Signer, InputMinterOptions) {
	t.Helper()
	t.Parallel()
	r := newRig(t)
	sg := serve.NewSigner([]byte("synthetic-media-key-for-tests-0001"), r.clk.Now)
	return r, sg, InputMinterOptions{Signer: sg, Store: r.st, Data: r.data, PublicURL: "https://mm.example.com/", Clock: r.clk}
}

func srcRequest(id, sha string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id":"x","task":"repaint","src":{"track_id":%q,"master_sha256":%q,"duration_ds":1000}}`, id, sha))
}

// verifyLink parses an input URL and runs the media route's verifier over it.
func verifyLink(t *testing.T, sg *serve.Signer, raw, wantSHA string) time.Time {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "mm.example.com" {
		t.Fatalf("link %q is not an absolute public URL", raw)
	}
	sha, ext, ok := strings.Cut(strings.TrimPrefix(u.Path, "/media/"), ".")
	if !ok || sha != wantSHA || ext != "flac" {
		t.Fatalf("link path %q, want %s.flac", u.Path, wantSHA)
	}
	q := u.Query()
	if err := sg.Verify(sha, ext, q.Get("exp"), q.Get("sig")); err != nil {
		t.Fatalf("the media route would refuse the link: %v", err)
	}
	var exp int64
	_, _ = fmt.Sscan(q.Get("exp"), &exp)
	return time.Unix(exp, 0).UTC()
}

func TestInputMinter_SourceAndReference(t *testing.T) {
	r, sg, o := minterRig(t)
	id, sha, size := masterOf(t, r)
	clipSHA := strings.Repeat("c", 64)
	if err := r.data.MkdirAll("derived/clips/cc", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.data.WriteFile("derived/clips/cc/"+clipSHA+".flac", []byte("0123456789"), 0o444); err != nil {
		t.Fatal(err)
	}
	o.Clips = clipResolver{clipSHA + ".flac": {RelPath: "derived/clips/cc/" + clipSHA + ".flac", ETag: clipSHA}}
	m := NewInputMinter(o)
	expires := r.clk.Now().Add(3*time.Hour + 17*time.Second)
	req := json.RawMessage(fmt.Sprintf(`{"id":"x","task":"text2music","src":{"track_id":%q,"master_sha256":%q,"duration_ds":1000},"ref":{"track_id":%q,"clip_sha256":%q,"from_ds":0,"to_ds":200}}`, id, sha, id, clipSHA))
	got, err := m.MintInputs(t.Context(), req, expires)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("inputs %v", got)
	}
	src, ref := got[provider.InputSrcAudio], got[provider.InputReferenceAudio]
	if src.SHA256 != sha || src.Bytes != size || ref.SHA256 != clipSHA || ref.Bytes != 10 {
		t.Errorf("digests: src %+v ref %+v", src, ref)
	}
	for _, c := range []struct {
		l   provider.InputRef
		sha string
	}{{src, sha}, {ref, clipSHA}} {
		if exp := verifyLink(t, sg, c.l.URL, c.sha); exp.Before(expires) || exp.After(expires.Add(2*linkBucket)) {
			t.Errorf("expiry %v must outlive %v by less than two buckets", exp, expires)
		}
	}
	// only a source: no reference entry
	only, err := m.MintInputs(t.Context(), srcRequest(id, sha), expires)
	if err != nil || len(only) != 1 || only[provider.InputSrcAudio].SHA256 != sha {
		t.Errorf("source only: %v %v", only, err)
	}
}

func TestInputMinter_NoInputsNilMap(t *testing.T) {
	_, _, o := minterRig(t)
	got, err := NewInputMinter(o).MintInputs(t.Context(), json.RawMessage(`{"id":"x","task":"text2music"}`), time.Now().Add(time.Hour))
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err = NewInputMinter(InputMinterOptions{}).MintInputs(t.Context(), json.RawMessage(`{"id":"yue"}`), time.Now()); got != nil || err != nil {
		t.Fatalf("an unwired minter must still answer nil for a request without inputs: %v %v", got, err)
	}
}

func TestInputMinter_Refusals(t *testing.T) {
	r, _, o := minterRig(t)
	id, sha, _ := masterOf(t, r)
	o.Clips = clipResolver{}
	m := NewInputMinter(o)
	expires := r.clk.Now().Add(time.Hour)
	for _, c := range []struct {
		name string
		req  json.RawMessage
		want error
	}{
		{"master changed", srcRequest(id, strings.Repeat("0", 64)), ErrInputChanged},
		{"unknown track", srcRequest("no-such-track", sha), ErrInputChanged},
		{"clip missing", json.RawMessage(fmt.Sprintf(`{"ref":{"track_id":%q,"clip_sha256":%q}}`, id, strings.Repeat("d", 64))), ErrInputChanged},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := m.MintInputs(t.Context(), c.req, expires)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if strings.Contains(err.Error(), "sig=") || strings.Contains(err.Error(), "/media/") {
				t.Errorf("error leaks a link: %v", err)
			}
		})
	}
	if _, err := NewInputMinter(InputMinterOptions{Store: r.st}).MintInputs(t.Context(), srcRequest(id, sha), expires); err == nil {
		t.Error("an unwired minter must refuse")
	}
}

// TestInputMinter_HorizonMatchesVerifier keeps maxLinkHorizon in step with serve.Signer.Verify: the longest window the minter
// signs verifies, one bucket more is refused up front.
func TestInputMinter_HorizonMatchesVerifier(t *testing.T) {
	r, sg, o := minterRig(t)
	id, sha, _ := masterOf(t, r)
	m := NewInputMinter(o)
	now := r.clk.Now()
	longest := now.Add(maxLinkHorizon - 2*linkBucket)
	got, err := m.MintInputs(t.Context(), srcRequest(id, sha), longest)
	if err != nil {
		t.Fatal(err)
	}
	verifyLink(t, sg, got[provider.InputSrcAudio].URL, sha)
	if _, err := m.MintInputs(t.Context(), srcRequest(id, sha), now.Add(maxLinkHorizon)); !errors.Is(err, ErrInputHorizon) {
		t.Errorf("a window past the horizon: %v", err)
	}
	// the verifier really refuses a link beyond 14 h, so the constant is not merely conservative nonsense
	far := sg.Sign(sha, "flac", maxLinkHorizon+time.Hour, time.Minute)
	u, _ := url.Parse(far.URL)
	if err := sg.Verify(sha, "flac", u.Query().Get("exp"), u.Query().Get("sig")); err == nil {
		t.Error("the verifier accepted a link past its own horizon")
	}
}

// acePlainRig is a runner rig that knows the ACE-Step engine and mints through the given minter.
func acePlainRig(t *testing.T, m InputMinter, opts ...func(*RunnerOptions)) *bridgeRig {
	t.Helper()
	t.Parallel()
	r := newRig(t, func(_ *rig, o *RunnerOptions) {
		o.Engines, o.Inputs = registry.Default(registry.Options{ACEStep: true}), m
		o.Expects = map[string]provider.WorkerExpect{"acestep15": {Weights: map[string]string{"ACE-Step/Ace-Step1.5": "0123456789abcdef0123456789abcdef01234567"}}}
		for _, f := range opts {
			f(o)
		}
	})
	return &bridgeRig{rig: r}
}

// dumpDB returns every text cell of every table, to look for something that must be nowhere.
func dumpDB(t *testing.T, r *rig) string {
	t.Helper()
	var sb strings.Builder
	rows, err := r.st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND sql NOT LIKE 'CREATE VIRTUAL%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		cols, err := r.st.R.Query(`SELECT name FROM pragma_table_info(?)`, tb)
		if err != nil {
			continue
		}
		var parts []string
		for cols.Next() {
			var c string
			_ = cols.Scan(&c)
			parts = append(parts, fmt.Sprintf(`COALESCE(CAST("%s" AS TEXT),'')`, c))
		}
		cols.Close()
		if len(parts) == 0 {
			continue
		}
		data, err := r.st.R.Query(fmt.Sprintf(`SELECT %s FROM "%s"`, strings.Join(parts, ` || '|' || `), tb))
		if err != nil {
			continue // a virtual or shadow table the reader cannot select from
		}
		for data.Next() {
			var s string
			_ = data.Scan(&s)
			sb.WriteString(s + "\n")
		}
		data.Close()
	}
	return sb.String()
}

func TestTakeSpec_InputsNeverPersisted(t *testing.T) {
	var minted provider.InputRef
	var m InputMinter
	b := acePlainRig(t, minterFunc(func(ctx context.Context, req json.RawMessage, exp time.Time) (map[string]provider.InputRef, error) {
		got, err := m.MintInputs(ctx, req, exp)
		minted = got[provider.InputSrcAudio]
		return got, err
	}))
	sg := serve.NewSigner([]byte("synthetic-media-key-for-tests-0001"), b.clk.Now)
	m = NewInputMinter(InputMinterOptions{Signer: sg, Store: b.st, Data: b.data, PublicURL: testPublicURL, Clock: b.clk})
	id, sha, _ := masterOf(t, b.rig)
	recipe := b.aceRecipe(aceRequest(t, map[string]any{"task": "repaint", "duration_ds": nil, "thinking": false,
		"src": map[string]any{"track_id": id, "master_sha256": sha, "duration_ds": 1000}, "repaint_from_ds": 100, "repaint_to_ds": 300}))
	job := b.seedACE(recipe)
	spec := b.specOf(job)

	u, err := url.Parse(spec.Inputs[provider.InputSrcAudio].URL)
	if err != nil || !strings.HasPrefix(u.String(), testPublicURL+"/media/"+sha+".flac?exp=") || u.Query().Get("sig") == "" {
		t.Fatalf("the worker did not get a signed link: %q", spec.Inputs[provider.InputSrcAudio].URL)
	}
	if spec.MinAudioS != 0 {
		t.Errorf("MinAudioS %d on an ACE-Step spec", spec.MinAudioS)
	}
	if spec.Expect.Weights["ACE-Step/Ace-Step1.5"] == "" {
		t.Errorf("the ACE-Step weights were not pinned: %+v", spec.Expect)
	}
	if err := checkSpecParses(spec); err != nil {
		t.Fatalf("the worker input does not parse: %v", err)
	}
	// nowhere in the database: no signature, no media path with a query, no expiry capability
	db := dumpDB(t, b.rig)
	for _, leak := range []string{u.Query().Get("sig"), "sig=", "exp=", minted.URL} {
		if strings.Contains(db, leak) {
			t.Fatalf("the database holds %q", leak)
		}
	}
	// nor in anything the service returns about the job
	raw, _ := json.Marshal(b.job(job))
	if strings.Contains(string(raw), "sig=") || strings.Contains(string(raw), "/media/") {
		t.Errorf("job JSON leaks an input link: %s", raw)
	}
	for _, e := range b.events(job) {
		if strings.Contains(e, "media") {
			t.Errorf("event %q", e)
		}
	}
}

type minterFunc func(ctx context.Context, request json.RawMessage, expires time.Time) (map[string]provider.InputRef, error)

func (f minterFunc) MintInputs(ctx context.Context, request json.RawMessage, expires time.Time) (map[string]provider.InputRef, error) {
	return f(ctx, request, expires)
}

// checkSpecParses runs the spec through the same strict parser the worker contract mirrors.
func checkSpecParses(spec provider.TakeSpec) error {
	raw, err := provider.BuildWorkerInput(spec)
	if err != nil {
		return err
	}
	_, err = provider.ParseWorkerInput(raw)
	return err
}

func TestSubmit_InputsWithoutMinterRefused(t *testing.T) {
	b := acePlainRig(t, nil)
	id, sha, _ := masterOf(t, b.rig)
	recipe := b.aceRecipe(aceRequest(t, map[string]any{"task": "repaint", "duration_ds": nil, "thinking": false,
		"src": map[string]any{"track_id": id, "master_sha256": sha, "duration_ds": 1000}, "repaint_from_ds": 100, "repaint_to_ds": 300}))
	job := b.seedACE(recipe)
	for range 4 {
		b.step()
		b.advance(3e9)
	}
	b.wantState(job, StateFailed)
	if got := b.job(job).ErrorCode; got != CodeSubmitRejected {
		t.Errorf("error code %q", got)
	}
	if n := len(b.be.Specs()); n != 0 {
		t.Errorf("%d specs reached the backend", n)
	}
	if strings.Contains(strings.Join(b.events(job), " "), "/media/") {
		t.Error("an event names a media path")
	}
}

func TestSubmit_Yue2NeverMintsInputs(t *testing.T) {
	called := false
	b := acePlainRig(t, minterFunc(func(context.Context, json.RawMessage, time.Time) (map[string]provider.InputRef, error) {
		called = true
		return nil, nil
	}))
	job := b.seed(batchSpec{recipe: testRecipe, mutation: `{"min_audio_s":120}`}).Jobs[0]
	spec := b.specOf(job)
	if called || spec.Inputs != nil {
		t.Errorf("minter called %v, inputs %v for a yue2 job", called, spec.Inputs)
	}
	if spec.MinAudioS != 120 {
		t.Errorf("yue2 MinAudioS %d, want 120 from the mutation", spec.MinAudioS)
	}
}

func TestSubmit_ACEStepIgnoresMinAudio(t *testing.T) {
	b := acePlainRig(t, nil)
	recipe := b.aceRecipe(aceRequest(t, nil))
	s := b.seed(batchSpec{recipe: recipe, mutation: `{"min_audio_s":120}`})
	b.exec(`DROP TRIGGER jobs_frozen`)
	b.exec(`DROP TRIGGER batches_frozen`)
	b.exec(`UPDATE jobs SET engine = 'acestep15' WHERE id = ?`, s.Jobs[0])
	b.exec(`UPDATE batches SET engine = 'acestep15' WHERE id = ?`, s.Batch)
	spec := b.specOf(s.Jobs[0])
	if spec.MinAudioS != 0 || spec.Inputs != nil {
		t.Errorf("MinAudioS %d inputs %v: an ACE-Step spec carries neither", spec.MinAudioS, spec.Inputs)
	}
	if _, ok := spec.Uploads["config.json"]; ok {
		t.Error("an ACE-Step attempt was given a config.json upload target")
	}
	for _, n := range []string{"audio.flac", "request.json", "result.json", "mm-result.json"} {
		if _, ok := spec.Uploads[n]; !ok {
			t.Errorf("no upload target for %s", n)
		}
	}
}

const mibBytes = 1 << 20

// claimPreflightSeen steps the first claim of a rig and returns the in-flight byte counts its preflight was asked about.
func claimPreflightSeen(t *testing.T, seed func(b *bridgeRig)) []int64 {
	t.Helper()
	var seen []int64
	b := acePlainRig(t, nil, func(o *RunnerOptions) {
		o.Preflight = func(_ context.Context, n int64) error { seen = append(seen, n); return nil }
	})
	seed(b)
	b.step()
	return seen
}

// TestPreflight_DiskSumsPerEngineMaxBytes: the claim preflight gets the sum of the candidate's own engine allowlist limits.
func TestPreflight_DiskSumsPerEngineMaxBytes(t *testing.T) {
	seen := claimPreflightSeen(t, func(b *bridgeRig) { b.seedACE(b.aceRecipe(aceRequest(t, nil))) })
	if want := int64(4*mibBytes + 4*mibBytes + 192*mibBytes + 64<<10); len(seen) != 1 || seen[0] != want {
		t.Errorf("ACE-Step claim preflight saw %v, want request 4 + result 4 + audio 192 MiB + mm-result 64 KiB = %d", seen, want)
	}
}

func TestPreflight_DiskSumsYue2Unchanged(t *testing.T) {
	seen := claimPreflightSeen(t, func(b *bridgeRig) { b.seed(batchSpec{recipe: testRecipe}) })
	if want := int64(373*mibBytes + 64<<10); len(seen) != 1 || seen[0] != want {
		t.Errorf("yue2 claim preflight saw %v, want the whole yue2 allowlist (373 MiB + mm-result 64 KiB) = %d", seen, want)
	}
}

func TestPreflight_MediaRoute(t *testing.T) {
	t.Parallel()
	free := func(string) (uint64, error) { return 5 << 30, nil }
	upload := func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/worker/v1/uploads/") {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"code":"UPLOAD_LINK_INVALID"}`))
			return true
		}
		return false
	}
	var mediaHits int
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upload(w, r) {
			return
		}
		mediaHits++
		sg := serve.NewSigner([]byte("k"), nil)
		serve.Handler(sg, nil, clipResolver{}, nil).ServeHTTP(w, r)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upload(w, r) {
			return
		}
		http.Error(w, "<html>", http.StatusBadGateway)
	}))
	defer bad.Close()

	// off: the media route is never asked, so a deployment without ACE-Step sees the M3 preflight exactly
	if err := NewPreflight(PreflightOptions{DataDir: t.TempDir(), PublicURL: good.URL, Free: free})(t.Context(), 0); err != nil || mediaHits != 0 {
		t.Fatalf("off: %v, %d media hits", err, mediaHits)
	}
	if err := NewPreflight(PreflightOptions{DataDir: t.TempDir(), PublicURL: good.URL, Free: free, CheckMedia: true})(t.Context(), 0); err != nil || mediaHits != 1 {
		t.Fatalf("on, healthy: %v, %d media hits", err, mediaHits)
	}
	var pe *PreflightError
	if err := NewPreflight(PreflightOptions{DataDir: t.TempDir(), PublicURL: bad.URL, Free: free, CheckMedia: true})(t.Context(), 0); !errors.As(err, &pe) || pe.Check != "media" {
		t.Errorf("on, broken: %v", err)
	}
}
