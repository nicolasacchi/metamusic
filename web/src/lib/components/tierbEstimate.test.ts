import { beforeAll, describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import EstimateView from "./EstimateView.svelte";
import ConfirmSpend from "./ConfirmSpend.svelte";
import AnalyzeButton from "./AnalyzeButton.svelte";
import TierBPanel from "./TierBPanel.svelte";
import { me } from "../me.svelte";

// The listening check in the estimate and confirm UI (M5b W1): badge per take, the skip option, cost and warnings in words.

const clean = (h: string) => h.replace(/<!--.*?-->/g, "");
const take = (i: number, o: object = {}) => ({ take_index: i, request_id: "r" + i, seed: i, title: "Alice " + i, operator: "new_seed", expected_audio_s: 120, timeout_s: 300, max_micro_usd: 100_000, max_usd: "0.10", engine: "yue2", cross_engine: false, ...o });
const base = (takes: object[], mode = "new_seed", o: object = {}) => ({
  engine: "yue2",
  backend: "fake",
  eta_s: 60,
  takes: takes.length,
  items: [{ index: 0, mode, takes }],
  warnings: [],
  requires_human_approval: false,
  approval_reasons: [],
  confirmation_token: "mmc_x",
  cost: { expected_micro_usd: 100_000, max_micro_usd: 200_000, retry_allowance_micro_usd: 0, ceiling_micro_usd: 300_000 },
  ...o,
});
const view = (estimate: object, extra: object = {}) => clean(render(EstimateView as never, { props: { estimate, ...extra } } as never).body);

beforeAll(() => {
  me.loaded = true;
  me.kind = "human";
  me.allowed = ["get_spend", "estimate_generation", "create_generation_human"];
});

describe("EstimateView listening check", () => {
  test("a take with the check and one without carry their badges and the reason", () => {
    const e = base([take(0, { tier_b: true }), take(1)], "new_seed", {
      warnings: [{ code: "tier_b_no_lyrics", field: "options.tier_b", message: "m", severity: "warning", take_index: 1 }],
    });
    const h = view(e, { onskip: () => {} });
    expect(h).toContain('data-testid="take-check" data-on="true"');
    expect(h).toContain('data-testid="take-check" data-on="false"');
    expect(h).toContain("1 of 2 takes get a listening check");
    expect(h).toContain("No listening check: the lyrics have no line to listen for.");
    expect(h).toContain('data-testid="skip-tierb"');
    expect(h).not.toMatch(/data-testid="skip-tierb"[^>]*checked/);
  });
  test("a closed worker image is said in plain words, not as a code", () => {
    const e = base([take(0)], "new_seed", { warnings: [{ code: "tier_b_unavailable", field: "options.tier_b", message: "m", severity: "warning", take_index: 0 }] });
    const h = view(e, { onskip: () => {} });
    expect(h).toContain("No listening check: the worker image is not open for it yet.");
    expect(h).not.toContain("<code>tier_b_unavailable</code>");
  });
  test("skipped shows the box checked and says so", () => {
    const h = view(base([take(0)]), { skipTierB: true, onskip: () => {} });
    expect(h).toContain("Skipped: no take gets a listening check.");
    expect(h).toMatch(/<input[^>]*checked[^>]*data-testid="skip-tierb"|<input[^>]*data-testid="skip-tierb"[^>]*checked/);
  });
  test("the cost of the check is the difference to the estimate without it, for someone who sees money", () => {
    const withoutE = base([take(0)], "new_seed", { cost: { expected_micro_usd: 70_000, max_micro_usd: 150_000, retry_allowance_micro_usd: 0, ceiling_micro_usd: 250_000 } });
    expect(view(base([take(0, { tier_b: true })]), { without: withoutE, onskip: () => {} })).toContain("The listening check adds up to $0.05 to the ceiling");
    me.allowed = ["estimate_generation"];
    expect(view(base([take(0, { tier_b: true })]), { without: withoutE, onskip: () => {} })).not.toContain("adds up to");
    me.allowed = ["get_spend", "estimate_generation", "create_generation_human"];
  });
  test("an analyze estimate is the check, offers no skip box and costs the whole job", () => {
    const h = view(base([take(0, { operator: "analyze" })], "analyze"));
    expect(h).toContain("Listens to the vocals of 1 take");
    expect(h).toContain("The listening check is the whole job: up to $0.30.");
    expect(h).not.toContain('data-testid="skip-tierb"');
  });
  test("another engine has no listening check section", () => {
    const h = view(base([take(0, { engine: "acestep15" })], "new_seed", { engine: "acestep15" }), { onskip: () => {} });
    expect(h).not.toContain('data-testid="tierb-estimate"');
  });
});

describe("ConfirmSpend", () => {
  test("an analyze batch says the money is for the listening check", () => {
    const h = clean(render(ConfirmSpend as never, { props: { estimate: base([take(0, { operator: "analyze" })], "analyze"), onbatch: () => {}, onreestimate: () => {} } } as never).body);
    expect(h).toContain("Spend up to $0.30 for the listening check");
  });
  test("a render batch keeps its plain label", () => {
    const h = clean(render(ConfirmSpend as never, { props: { estimate: base([take(0)]), onbatch: () => {}, onreestimate: () => {} } } as never).body);
    expect(h).toContain("Spend up to $0.30");
    expect(h).not.toContain("for the listening check");
  });
});

describe("AnalyzeButton and TierBPanel", () => {
  test("the button is there for someone who may estimate, and goes nowhere on its own", () => {
    me.allowed = ["estimate_generation"];
    const h = clean(render(AnalyzeButton as never, { props: { trackId: "t1", go: () => {} } } as never).body);
    expect(h).toContain('data-testid="analyze-button"');
    expect(h).toContain("confirm before anything is spent");
  });
  test("no button without estimate_generation", () => {
    me.allowed = ["get_track_tier_b"];
    const h = clean(render(AnalyzeButton as never, { props: { trackId: "t1", go: () => {} } } as never).body);
    expect(h).not.toContain("analyze-button");
  });
  test("the panel waits for the dossier before it shows anything", () => {
    const h = clean(render(TierBPanel as never, { props: { trackId: "t1", dossier: null, go: () => {} } } as never).body);
    expect(h).toContain('data-testid="tierb-panel"');
    expect(h).toContain("Loading");
  });
});
