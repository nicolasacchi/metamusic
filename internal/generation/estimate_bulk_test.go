package generation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// BULK-P6 (C398.1, C393.1): the estimate of a pod batch.

var (
	editorP   = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", Via: "proxy", Scopes: auth.ScopesFor(auth.RoleEditor)}
	listenerP = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", Name: "carol", Via: "proxy", Scopes: auth.ScopesFor(auth.RoleListener)}
	deviceP   = auth.Principal{Kind: auth.KindDevice, Role: auth.RoleOwner, ID: "dev-1", Name: "alice phone", Via: "device", User: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	nightP    = auth.Principal{Kind: auth.KindAgent, ID: "night-shift", Name: "night shift", Via: "token", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}
	connP     = auth.Principal{Kind: auth.KindAgent, ID: "conn-1", Name: "connector", Via: "oauth", Client: "client-1", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}
)

func (x *rigE2) podEstimate(p auth.Principal, in generation.EstimateIn) (generation.Estimate, error) {
	if in.Via == "" {
		in.Via = viaFor(p)
	}
	return x.svc.Estimate(context.Background(), p, in)
}

// podTake is the part of a stored take the pod tests read.
type podTake struct {
	Needs            []string `json:"needs"`
	TimeoutS         int      `json:"timeout_s"`
	TakeMicroUSD     int64    `json:"take_micro_usd"`
	OverheadMicroUSD int64    `json:"attempt_overhead_micro_usd"`
}

func (x *rigE2) podTakes(e generation.Estimate) []podTake {
	x.t.Helper()
	var out []podTake
	if err := json.Unmarshal([]byte(x.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID)), &out); err != nil {
		x.t.Fatal(err)
	}
	return out
}

// podRig is a rig with the simulated pod backend, armed.
func podRig(t *testing.T) *rigE2 {
	t.Helper()
	x := newRigE2(t)
	x.withPodBackend(provider.ModeFake, true)
	x.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES (?, 1, 'system:test', ?)
		ON CONFLICT(backend) DO UPDATE SET armed = 1`, provider.BackendFakePod, store.FormatTime(x.clk.Now()))
	return x
}

// cacheFreshQuote stores q as the cached quote of the named backend, observed now (the rig clock), armed.
func (x *rigE2) cacheFreshQuote(backend string, q provider.Quote) {
	x.t.Helper()
	q.ObservedAt = x.clk.Now()
	b, _ := json.Marshal(q)
	at := store.FormatTime(q.ObservedAt)
	x.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES (?, 1, 'system:test', ?, ?, ?)
		ON CONFLICT(backend) DO UPDATE SET armed = 1, quote_json = excluded.quote_json, quote_at = excluded.quote_at`, backend, at, string(b), at)
}

func podIn(x *rigE2, takes int) generation.EstimateIn {
	return generation.EstimateIn{Backend: provider.BackendFakePod, Items: podItems(x, takes)}
}

func TestEstimate_PodTakeLimits(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	for _, tc := range []struct {
		takes int
		code  string
		want  int
	}{{1, "BULK_TOO_SMALL", 422}, {29, "BULK_TOO_SMALL", 422}, {30, "", 0}, {100, "", 0}, {101, "BATCH_TOO_LARGE", 422}} {
		_, err := x.podEstimate(human, podIn(x, tc.takes))
		if tc.code == "" {
			if err != nil {
				t.Errorf("%d takes: %v", tc.takes, err)
			}
			continue
		}
		pe := wantProblem(t, err, tc.want, tc.code)
		if tc.takes == 101 && pe.Details["limit"] != 100 || tc.takes < 30 && pe.Details["minimum"] != 30 {
			t.Errorf("%d takes: details %v", tc.takes, pe.Details)
		}
	}
	// the 50-take limit still applies to every other backend, and a pod count is no way round it
	_, err := x.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: podItems(x, 60), Via: spend.ViaHumanCreate})
	if pe := wantProblem(t, err, 422, "BATCH_TOO_LARGE"); pe.Details["limit"] != generation.MaxTakes {
		t.Errorf("serverless details %v", pe.Details)
	}
	// the canary is exactly one take
	_, err = x.podEstimate(human, generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(x, 2)})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	if _, err := x.podEstimate(human, generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(x, 1)}); err != nil {
		t.Errorf("canary: %v", err)
	}
}

func TestPodEstimate_WhoMay(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	written := x.count(`SELECT COUNT(*) FROM estimates`)
	for _, tc := range []struct {
		name   string
		p      auth.Principal
		status int
		code   string
	}{
		{"owner human", human, 0, ""},
		{"owner via an API token", agent, 403, "HUMAN_ONLY"},
		{"agent token", other, 403, "HUMAN_ONLY"},
		{"night-shift-style token", nightP, 403, "HUMAN_ONLY"},
		{"device", deviceP, 403, "HUMAN_ONLY"},
		{"editor", editorP, 403, "OWNER_ONLY"},
		{"listener", listenerP, 403, "OWNER_ONLY"},
		{"connector", connP, 403, "HUMAN_ONLY"},
	} {
		_, err := x.podEstimate(tc.p, podIn(x, 30))
		if tc.code == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		pe := wantProblem(t, err, tc.status, tc.code)
		if pe.Details["reason"] != "bulk" {
			t.Errorf("%s: details %v", tc.name, pe.Details)
		}
	}
	// nothing was written for a refusal
	if n := x.count(`SELECT COUNT(*) FROM estimates`) - written; n != 1 { // the Owner's own
		t.Errorf("%d estimates written, want the Owner's one", n)
	}
	// the canary is the one thing a token may ask; an Editor, a device and the pod size rules still apply to it
	for _, p := range []auth.Principal{human, agent, nightP, connP} {
		if _, err := x.podEstimate(p, generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(x, 1)}); err != nil {
			t.Errorf("canary as %s: %v", p.Key(), err)
		}
	}
	for _, p := range []auth.Principal{editorP, listenerP} {
		_, err := x.podEstimate(p, generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(x, 1)})
		wantProblem(t, err, 403, "OWNER_ONLY")
	}
	_, err := x.podEstimate(deviceP, generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(x, 1)})
	wantProblem(t, err, 403, "HUMAN_ONLY")
	// a token's canary is not a way to a full batch
	_, err = x.podEstimate(agent, generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(x, 30)})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
}

func TestPodEstimate_RefusesAnalyzeAndPlan(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	for name, it := range map[string]generation.Item{
		"analyze":     {Mode: lineage.ModeAnalyze, TrackIDs: []string{x.info.Takes["T1"]}},
		"render_plan": {Mode: lineage.ModeRenderPlan, Desk: &generation.DeskItem{PlanIDs: []string{"plan-1"}}},
	} {
		_, err := x.podEstimate(human, generation.EstimateIn{Backend: provider.BackendFakePod, Items: []generation.Item{it}})
		pe := wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
		if pe.Details["backend"] != provider.BackendFakePod {
			t.Errorf("%s: details %v", name, pe.Details)
		}
	}
	// other engines are not routed to a pod at all
	_, err := x.podEstimate(human, generation.EstimateIn{Engine: aceName, Backend: provider.BackendFakePod, Items: []generation.Item{parentItem(lineage.ModePort, x.info.Takes["T1"])}})
	wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
}

func podQuote(digest string, at time.Time) provider.Quote {
	return provider.Quote{Backend: provider.BackendFakePod, ListPricePPH: 350_000, Pools: []string{}, GPUClasses: []string{"gpu-a", "gpu-b"}, ObservedAt: at,
		Endpoint: provider.EndpointConfig{Name: provider.BackendFakePod, Type: provider.EndpointTypePod, WorkersMax: 1, GPUCount: 1, ImageDigest: digest}}
}

// realPodRig is a rig whose pod backend is the real kind: runpod mode, a cached quote with an image digest and the canary rule on.
func realPodRig(t *testing.T) (*rigE2, string) {
	t.Helper()
	x := newRigE2(t)
	x.withPodBackend(provider.ModeRunPod, true)
	cfg := testPodConfig()
	cfg.RequireCanary = true
	x.svc.UsePods(cfg)
	digest := "sha256:" + strings.Repeat("a", 64)
	x.cacheFreshQuote(provider.BackendFakePod, podQuote(digest, x.clk.Now()))
	return x, digest
}

func (x *rigE2) canaryRow(digest, verdict string) {
	x.t.Helper()
	x.exec(`INSERT INTO pod_safety_log(kind, image_digest, verdict, detail_json, created_by, created_at) VALUES ('canary', ?, ?, '{}', 'system:bulk', ?)`,
		digest, verdict, store.FormatTime(x.clk.Now()))
}

func TestPodEstimate_SafetyGate(t *testing.T) {
	t.Parallel()
	x, digest := realPodRig(t)
	canary := generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(x, 1)}
	reason := func(err error) string {
		pe := wantProblem(t, err, 503, "GENERATION_DISABLED")
		return pe.Details["reason"].(string)
	}
	// no canary row: ordinary estimates are refused, the canary itself is allowed
	_, err := x.podEstimate(human, podIn(x, 30))
	if got := reason(err); got != generation.ReasonPodSafetyUnverified {
		t.Errorf("no row: reason %q", got)
	}
	if _, err := x.podEstimate(human, canary); err != nil {
		t.Errorf("canary before any verdict: %v", err)
	}
	for _, v := range []string{"failed", "inconclusive"} {
		x.canaryRow(digest, v)
		_, err = x.podEstimate(human, podIn(x, 30))
		if got := reason(err); got != generation.ReasonPodSafetyUnverified {
			t.Errorf("%s: reason %q", v, got)
		}
	}
	// a verdict for another digest does not count
	x.canaryRow("sha256:"+strings.Repeat("b", 64), "verified")
	if _, err = x.podEstimate(human, podIn(x, 30)); err == nil {
		t.Error("a verified canary of another image opened the gate")
	}
	x.canaryRow(digest, "verified")
	e, err := x.podEstimate(human, podIn(x, 30))
	if err != nil {
		t.Fatalf("verified: %v", err)
	}
	if e.Backend != provider.BackendFakePod || e.Pod == nil {
		t.Errorf("estimate %+v", e)
	}
	// the latest row wins: a later failure closes it again
	x.canaryRow(digest, "failed")
	_, err = x.podEstimate(human, podIn(x, 30))
	reason(err)
	// a stale quote carries no digest at all
	x.clk.Advance(2 * time.Hour)
	_, err = x.podEstimate(human, podIn(x, 30))
	wantProblem(t, err, 503, "GENERATION_DISABLED")
}

func TestPodEstimate_NeedsAgent(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	e, err := x.podEstimate(human, podIn(x, 30))
	if err != nil {
		t.Fatal(err)
	}
	docs := x.podTakes(e)
	if len(docs) != 30 {
		t.Fatalf("%d takes", len(docs))
	}
	for i, d := range docs {
		if !slices.Contains(d.Needs, "agent") || !slices.IsSorted(d.Needs) {
			t.Fatalf("take %d needs %v", i, d.Needs)
		}
	}
	// a take's own needs stay next to agent, in canonical order
	in := podIn(x, 29)
	in.Items = append(in.Items, v2Item())
	e, err = x.podEstimate(human, in)
	if err != nil {
		t.Fatal(err)
	}
	last := x.podTakes(e)[29]
	if strings.Join(last.Needs, ",") != "agent,v2" {
		t.Errorf("v2 take needs %v", last.Needs)
	}
	// and the same estimate on the serverless route does not gain agent
	plain, err := x.estimateVia("", v2Item())
	if err != nil {
		t.Fatal(err)
	}
	if got := x.podTakes(plain)[0].Needs; strings.Join(got, ",") != "v2" {
		t.Errorf("serverless needs %v", got)
	}
	// a closed agent capability refuses the estimate before anything is stored
	if err := provider.AppendContract(context.Background(), x.st.W, provider.WorkerContractRow{Backend: provider.BackendFakePod, ImageDigest: "sha256:" + strings.Repeat("4", 64),
		Caps: nil, Via: provider.ContractViaCLI, ChangedBy: "system:cli", ChangedAt: x.clk.Now(), Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	before := x.count(`SELECT COUNT(*) FROM estimates`)
	_, err = x.podEstimate(human, podIn(x, 30))
	pe := wantProblem(t, err, 503, "GENERATION_DISABLED")
	if pe.Details["reason"] != generation.ReasonWorkerOutdated || !strings.Contains(strings.Join(toStrings(pe.Details["missing"]), ","), "agent") {
		t.Errorf("details %v", pe.Details)
	}
	if x.count(`SELECT COUNT(*) FROM estimates`) != before {
		t.Error("an estimate was stored")
	}
}

// TestPodEstimate_QuotedByQuotePod: the money of a pod estimate is QuotePod's, in integer micro-USD, with the pod overhead inside the
// batch maximum and one re-creation inside the ceiling; the canary is one take with no re-creation.
func TestPodEstimate_QuotedByQuotePod(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	e, err := x.podEstimate(human, podIn(x, 30))
	if err != nil {
		t.Fatal(err)
	}
	price := e.PricePerHour
	if price != 690_000 { // the simulated pod's list price, no margin in the rig
		t.Fatalf("price %d", price)
	}
	f := func(s int) money.MicroUSD { return money.ForSeconds(int64(s), price) }
	docs := x.podTakes(e)
	var sumTake, maxTake money.MicroUSD
	var total, tmax int
	for _, d := range docs {
		sumTake += money.MicroUSD(d.TakeMicroUSD)
		maxTake = max(maxTake, money.MicroUSD(d.TakeMicroUSD))
		total += d.TimeoutS
		tmax = max(tmax, d.TimeoutS)
		if d.OverheadMicroUSD != 0 {
			t.Fatalf("a take carries an attempt overhead of %d", d.OverheadMicroUSD)
		}
	}
	overheadS := 1200 + 120 + 300 + 15 + 330
	if e.Cost.Max != sumTake+f(overheadS) || e.Cost.RetryAllowance != maxTake+f(overheadS) || e.Cost.Ceiling != e.Cost.Max+e.Cost.RetryAllowance {
		t.Errorf("cost %+v, takes %d, overhead %d", e.Cost, sumTake, f(overheadS))
	}
	if e.Cost.AttemptOverhead != 0 {
		t.Errorf("attempt overhead %d", e.Cost.AttemptOverhead)
	}
	if want := 1200 + total + tmax + 120 + 300; e.Pod == nil || e.Pod.DeadlineS != want || e.Pod.BootS != 1200 {
		t.Errorf("pod block %+v, want deadline %d", e.Pod, want)
	}
	if !strings.HasPrefix(x.str(`SELECT quote_json FROM estimates WHERE id = ?`, e.ID), `{"price_pph_micro_usd":690000`) {
		t.Error("quote_json layout moved")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(x.str(`SELECT quote_json FROM estimates WHERE id = ?`, e.ID)), &raw); err != nil {
		t.Fatal(err)
	}
	var pod map[string]any
	if err := json.Unmarshal(raw["pod"], &pod); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]float64{"boot_s": 1200, "idle_s": 120, "slack_s": 300, "overrun_s": 330, "max_recreate": 1, "deadline_s": float64(e.Pod.DeadlineS)} {
		if pod[k] != want {
			t.Errorf("quote.pod.%s = %v, want %v", k, pod[k], want)
		}
	}
	if _, has := pod["canary"]; has {
		t.Error("an ordinary pod quote says canary")
	}
	if g := pod["gpu_types"].([]any); len(g) != 2 || g[0] != "gpu-a" {
		t.Errorf("gpu_types %v", g)
	}
	if w, a := x.val(`SELECT workers_max FROM estimates WHERE id = ?`, e.ID), x.val(`SELECT idle_s FROM estimates WHERE id = ?`, e.ID); w != 1 || a != 1 {
		t.Errorf("workers_max %d idle_s %d", w, a)
	}
	if string(raw["attempt_overhead_s"]) != "0" || string(raw["cold_start_s"]) != "0" || string(raw["idle_s"]) != "1" {
		t.Errorf("quote doc %s %s %s", raw["attempt_overhead_s"], raw["cold_start_s"], raw["idle_s"])
	}

	// the canary: one take, one pod, no re-creation
	c, err := x.podEstimate(human, generation.EstimateIn{PodCanary: "self_terminate", Backend: provider.BackendFakePod, Items: podItems(x, 1)})
	if err != nil {
		t.Fatal(err)
	}
	cd := x.podTakes(c)[0]
	if c.Cost.Max != money.MicroUSD(cd.TakeMicroUSD)+f(overheadS) || c.Cost.RetryAllowance != money.MicroUSD(cd.TakeMicroUSD) {
		t.Errorf("canary cost %+v take %d", c.Cost, cd.TakeMicroUSD)
	}
	var cpod struct {
		Pod struct {
			MaxRecreate int  `json:"max_recreate"`
			Canary      bool `json:"canary"`
		} `json:"pod"`
	}
	if err := json.Unmarshal([]byte(x.str(`SELECT quote_json FROM estimates WHERE id = ?`, c.ID)), &cpod); err != nil || !cpod.Pod.Canary || cpod.Pod.MaxRecreate != 0 {
		t.Errorf("canary quote.pod %+v %v", cpod, err)
	}
	if got := x.val(`SELECT json_extract(quote_json, '$.pod.canary') FROM estimates WHERE id = ?`, c.ID); got != 1 {
		t.Errorf("json_extract canary = %d", got)
	}
}

// TestPodEstimate_RuntimeRefused: a batch that cannot finish inside MM_POD_MAX_RUNTIME is 422 BATCH_TOO_LARGE reason pod_runtime.
func TestPodEstimate_RuntimeRefused(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	cfg := testPodConfig()
	cfg.MaxRuntimeS = 3600
	x.svc.UsePods(cfg)
	_, err := x.podEstimate(human, podIn(x, 30))
	pe := wantProblem(t, err, 422, "BATCH_TOO_LARGE")
	if pe.Details["reason"] != "pod_runtime" {
		t.Errorf("details %v", pe.Details)
	}
}

// TestPodEstimate_UnconfiguredIsRefused: a service that was never given the pod configuration prices no pod batch (it would be priced
// by the serverless path).
func TestPodEstimate_UnconfiguredIsRefused(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	x.withPodBackend(provider.ModeFake, true)
	o := x.opts
	o.Gate = x.gate(provider.ModeFake)
	svc := generation.New(o)
	svc.UseEngines(x.backends, x.gates, x.adoption, x.clipper)
	_, err := svc.Estimate(context.Background(), human, generation.EstimateIn{Backend: provider.BackendFakePod, Items: podItems(x, 30), Via: spend.ViaHumanCreate})
	wantProblem(t, err, 503, "UNAVAILABLE")
}

// TestPodEstimate_CanaryNeedsOwnerApproval: a canary estimate says it needs approval for every principal, and its batch waits for the
// Owner whoever created it, on either path, and a mandate never stands in for it.
func TestPodEstimate_CanaryNeedsOwnerApproval(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	in := generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(x, 1)}
	for _, p := range []auth.Principal{human, agent} {
		e, err := x.podEstimate(p, in)
		if err != nil {
			t.Fatal(err)
		}
		if !e.RequiresApproval || !strings.Contains(strings.Join(e.ApprovalReasons, ","), spend.ReasonPodCanary) {
			t.Errorf("%s: requires %v reasons %v", p.Key(), e.RequiresApproval, e.ApprovalReasons)
		}
		via := spend.ViaAPI
		if p.IsHuman() {
			via = spend.ViaHumanCreate // the Owner's own click is not an approval of a canary
		}
		c := x.mustCreate(p, e, via)
		if c.State != "awaiting_confirmation" || c.ApprovalState != "pending" {
			t.Errorf("%s: batch %s approval %s", p.Key(), c.State, c.ApprovalState)
		}
		if got := x.str(`SELECT reasons FROM approvals WHERE batch_id = ?`, c.BatchID); !strings.Contains(got, spend.ReasonPodCanary) {
			t.Errorf("%s: approval reasons %s", p.Key(), got)
		}
	}
	// an ordinary pod batch by the Owner is approved by the Owner's click, as any human_create batch
	e, err := x.podEstimate(human, podIn(x, 30))
	if err != nil || e.RequiresApproval {
		t.Fatalf("ordinary: %v requires %v", err, e.RequiresApproval)
	}
	if c := x.mustCreate(human, e, spend.ViaHumanCreate); c.State != "active" {
		t.Errorf("ordinary batch %s", c.State)
	}
}

// TestPodEstimate_NeverMandateCovered: an estimate that names a mandate is refused on a pod backend before anything is read from the
// mandate, and a canary stays refused with one.
func TestPodEstimate_NeverMandateCovered(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	x.svc.UseMandates(coveringMandates{})
	for _, tc := range []struct {
		p  auth.Principal
		in generation.EstimateIn
	}{{human, podIn(x, 30)}, {agent, generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(x, 1)}}} {
		tc.in.MandateID = "mandate-1"
		_, err := x.podEstimate(tc.p, tc.in)
		pe := wantProblem(t, err, 422, "VALIDATION_FAILED")
		if pe.Details["rule"] != "mandate_not_on_pods" {
			t.Errorf("details %v", pe.Details)
		}
		tc.in.Via = viaFor(tc.p)
		_, err = x.svc.Preview(context.Background(), tc.p, tc.in)
		wantProblem(t, err, 422, "VALIDATION_FAILED")
	}
}

// coveringMandates is a mandate that would cover anything: a pod estimate must be refused before it is asked.
type coveringMandates struct{}

func (coveringMandates) Cover(context.Context, lineage.Querier, auth.Principal, string, string, []generation.MandateTake, money.MicroUSD, bool) (bool, []string, error) {
	return true, nil, nil
}

func (coveringMandates) Recheck(context.Context, *sql.Tx, auth.Principal, string, string, []generation.MandateTake, money.MicroUSD, bool) error {
	return nil
}

// TestPodEstimate_QuoteDocByteIdenticalElsewhere: the pod terms exist only in a pod quote; every other estimate stores the quote
// document it always stored.
func TestPodEstimate_QuoteDocByteIdenticalElsewhere(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	e, err := x.estimateVia("", newSeedItem(x.info.Takes["T1"], 2))
	if err != nil {
		t.Fatal(err)
	}
	if e.Pod != nil {
		t.Errorf("pod block on a serverless estimate: %+v", e.Pod)
	}
	q := x.str(`SELECT quote_json FROM estimates WHERE id = ?`, e.ID)
	if strings.Contains(q, `"pod"`) || !strings.HasSuffix(q, `"price_observed_at":null}`) {
		t.Errorf("quote doc %s", q)
	}
	// options.pod_canary on a process with no pod backend is the same BACKEND_UNAVAILABLE as options.backend
	y := newRigE2(t)
	_, err = y.podEstimate(human, generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(y, 1)})
	wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
	_, err = y.podEstimate(agent, generation.EstimateIn{PodCanary: "self_terminate", Items: podItems(y, 1)})
	wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
	_, err = y.podEstimate(human, generation.EstimateIn{PodCanary: "other", Items: podItems(y, 1)})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	_, err = y.podEstimate(human, generation.EstimateIn{PodCanary: "self_terminate", Backend: provider.BackendFake, Items: podItems(y, 1)})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
}

// TestCreate_PodBatchFrozen: the batch of a pod estimate freezes one worker, no cold start or attempt overhead and the pod's money,
// and its jobs carry the agent need.
func TestCreate_PodBatchFrozen(t *testing.T) {
	t.Parallel()
	x := podRig(t)
	e, err := x.podEstimate(human, podIn(x, 30))
	if err != nil {
		t.Fatal(err)
	}
	c := x.mustCreate(human, e, spend.ViaHumanCreate)
	var workers, idle, overheadS, takes int64
	var backend string
	if err := x.st.R.QueryRow(`SELECT workers_max, idle_s, attempt_overhead_s, n_takes, backend FROM batches WHERE id = ?`, c.BatchID).Scan(&workers, &idle, &overheadS, &takes, &backend); err != nil {
		t.Fatal(err)
	}
	if workers != 1 || idle != 1 || overheadS != 0 || takes != 30 || backend != provider.BackendFakePod {
		t.Errorf("batch: workers %d idle %d overhead_s %d takes %d backend %s", workers, idle, overheadS, takes, backend)
	}
	if n := x.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND needs_json LIKE '%"agent"%'`, c.BatchID); n != 30 {
		t.Errorf("%d jobs with the agent need", n)
	}
	if got := x.val(`SELECT max_micro_usd FROM batches WHERE id = ?`, c.BatchID); got != int64(e.Cost.Max) {
		t.Errorf("batch max %d, estimate %d", got, e.Cost.Max)
	}
	// the reserve at create is the take reserves only; the pod overhead is reserved when a pod is created
	var reserved int64
	for _, d := range x.podTakes(e) {
		reserved += d.TakeMicroUSD
	}
	if got := x.val(`SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = ? AND kind = 'reserve'`, c.BatchID); got != reserved {
		t.Errorf("reserved %d, takes %d", got, reserved)
	}
}

// TestPods_ConfigRoundTrip: the service reports the pod configuration it was given, a copy of it.
func TestPods_ConfigRoundTrip(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	if _, ok := x.svc.Pods(); ok {
		t.Fatal("a service with no pod configuration reports one")
	}
	cfg := testPodConfig()
	x.svc.UsePods(cfg)
	cfg.GPUTypes[0] = "changed" // the service holds its own copy
	got, ok := x.svc.Pods()
	if !ok || got.MinTakes != 30 || got.GPUTypes[0] != "gpu-a" {
		t.Fatalf("config %+v %v", got, ok)
	}
	got.GPUTypes[0] = "changed"
	if again, _ := x.svc.Pods(); again.GPUTypes[0] != "gpu-a" {
		t.Error("the returned slice aliases the service's")
	}
}

// TestPodSafety_Verdicts: the latest canary row for the digest decides; no digest and no row are unverified.
func TestPodSafety_Verdicts(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	ctx := context.Background()
	digest := "sha256:" + strings.Repeat("c", 64)
	for _, tc := range []struct{ digest, want string }{{"", generation.PodSafetyUnverified}, {digest, generation.PodSafetyUnverified}} {
		if got, err := generation.PodSafety(ctx, x.st.R, tc.digest); err != nil || got != tc.want {
			t.Errorf("%q: %q %v", tc.digest, got, err)
		}
	}
	x.canaryRow(digest, "inconclusive")
	x.canaryRow(digest, "verified")
	if got, _ := generation.PodSafety(ctx, x.st.R, digest); got != generation.PodSafetyVerified {
		t.Errorf("latest row: %q", got)
	}
	x.exec(`DROP TABLE pod_safety_log`) // an unreadable table is an error, never a verdict
	if got, err := generation.PodSafety(ctx, x.st.R, digest); err == nil {
		t.Errorf("unreadable table: %q", got)
	}
}
