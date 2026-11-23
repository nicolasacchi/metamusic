import { describe, expect, test } from "bun:test";
import { intentToEstimate, loadIntent, saveIntent, validateIntent } from "./genIntent";
import {
  buildPlanSeeds, planSeedsGate, renderIntent, renderRows, renderStateText, RENDER_SEEDS_MAX, SEEDS_DEFAULT, SEEDS_MAX, planJobsHref, planJobLabel, renderGate, planSeedsRefusalText, keepRequest,
} from "./deskRender";

describe("renderIntent", () => {
  test("one plan, one seed: an ordinary generation item with the plan ids in desk", () => {
    const i = renderIntent(["p1"]);
    expect(i).toEqual({ mode: "render_plan", desk: { plan_ids: ["p1"] } });
    expect(validateIntent(i)).toBeNull();
    const e = intentToEstimate(i);
    expect(e.kind).toBe("generation");
    if (e.kind === "generation") expect(e.body.items).toEqual([{ mode: "render_plan", desk: { plan_ids: ["p1"] } }]);
  });
  test("extra seeds are the item's own n_seeds, at most 4", () => {
    expect(renderIntent(["p1"], 3)).toEqual({ mode: "render_plan", n_seeds: 3, desk: { plan_ids: ["p1"] } });
    expect(RENDER_SEEDS_MAX).toBe(4);
    expect(validateIntent(renderIntent(["p1"], 5))).toMatch(/at most 4/);
  });
  test("plan ids are 1..10 and unique", () => {
    expect(validateIntent({ mode: "render_plan", desk: { plan_ids: [] } })).not.toBeNull();
    const eleven = Array.from({ length: 11 }, (_, k) => `p${k}`);
    expect(validateIntent({ mode: "render_plan", desk: { plan_ids: eleven } })).toMatch(/at most 10/);
    expect(validateIntent({ mode: "render_plan", desk: { plan_ids: ["a", "a"] } })).not.toBeNull();
  });
  test("survives sessionStorage; a render intent without plan ids is dropped", () => {
    const m = new Map<string, string>();
    const s = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) };
    saveIntent(s, renderIntent(["p1", "p2"], 2));
    expect(loadIntent(s)).toEqual(renderIntent(["p1", "p2"], 2));
    saveIntent(s, { mode: "render_plan" });
    expect(loadIntent(s)).toBeNull();
  });
});

describe("buildPlanSeeds", () => {
  const form = (o: Partial<{ base: string; seeds: string; cot: "" | "full" | "melody" }> = {}) => ({ base: "t1", seeds: String(SEEDS_DEFAULT), cot: "" as const, ...o });
  test("eight seeds by default, the take as the base, no cot unless chosen", () => {
    expect(SEEDS_DEFAULT).toBe(8);
    const b = buildPlanSeeds(form());
    expect(b.errors).toEqual({});
    expect(b.intent).toEqual({ mode: "plan_seeds", parent_track_id: "t1", desk: { n_seeds: 8 } });
    expect(validateIntent(b.intent!)).toBeNull();
  });
  test("a chosen cot is sent; the base is trimmed", () => {
    expect(buildPlanSeeds(form({ base: " t9 ", cot: "melody", seeds: "3" })).intent).toEqual({ mode: "plan_seeds", parent_track_id: "t9", desk: { n_seeds: 3, cot: "melody" } });
  });
  test("per-field errors: no base, seeds outside 1..16, not a number", () => {
    expect(buildPlanSeeds(form({ base: " " })).errors.base).toBeDefined();
    expect(buildPlanSeeds(form({ seeds: "0" })).errors.seeds).toBeDefined();
    expect(buildPlanSeeds(form({ seeds: String(SEEDS_MAX + 1) })).errors.seeds).toBeDefined();
    expect(buildPlanSeeds(form({ seeds: "2.5" })).errors.seeds).toBeDefined();
    expect(buildPlanSeeds(form({ seeds: "x" })).intent).toBeNull();
    expect(buildPlanSeeds(form({ seeds: "16" })).errors).toEqual({});
  });
  test("plan_seeds is one item whatever the seed count (the jobs split server-side)", () => {
    expect(validateIntent({ mode: "plan_seeds", parent_track_id: "t1", desk: { n_seeds: 16 } })).toBeNull();
    expect(validateIntent({ mode: "plan_seeds", desk: { n_seeds: 4 } })).not.toBeNull();
    expect(validateIntent({ mode: "plan_seeds", parent_track_id: "t1", desk: { n_seeds: 17 } })).not.toBeNull();
    const m = new Map<string, string>();
    const s = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) };
    saveIntent(s, buildPlanSeeds(form()).intent!);
    expect(loadIntent(s)?.mode).toBe("plan_seeds");
  });
});

describe("planSeedsGate", () => {
  const owner = ["estimate_generation", "create_generation_human", "get_provider_status"];
  test("hidden for a principal that cannot estimate", () => {
    expect(planSeedsGate(["list_plans"], ["plan", "v2"]).kind).toBe("hidden");
  });
  test("ready when the worker is open for plan and v2, or when the caps are not known", () => {
    expect(planSeedsGate(owner, ["plan", "v2", "attach"]).kind).toBe("ready");
    expect(planSeedsGate(owner, null).kind).toBe("ready");
  });
  test("closed, in words, naming what is missing (production: every capability closed)", () => {
    const g = planSeedsGate(owner, []);
    expect(g.kind).toBe("closed");
    if (g.kind === "closed") {
      expect(g.reason).toMatch(/not available/i);
      expect(g.reason).toContain("plan");
      expect(g.reason).toContain("v2");
    }
    const one = planSeedsGate(owner, ["v2"]);
    if (one.kind === "closed") expect(one.reason).toContain("plan");
    else throw new Error("expected closed");
  });
});

describe("renderGate", () => {
  test("a render needs no capability: ready for whoever may estimate, hidden otherwise", () => {
    expect(renderGate(["estimate_generation"]).kind).toBe("ready");
    expect(renderGate(["create_generation"]).kind).toBe("ready");
    expect(renderGate(["create_generation_human"]).kind).toBe("ready");
    expect(renderGate(["list_plans"]).kind).toBe("hidden");
  });
});

describe("renders on a plan", () => {
  test("states in words and the take link", () => {
    expect(renderStateText("completed")).toBe("done");
    expect(renderStateText("awaiting_confirmation")).toBe("waiting for the Owner");
    expect(renderStateText("queued")).toBe("queued");
    expect(renderStateText("failed")).toBe("failed");
    expect(renderStateText("whatever")).toBe("whatever");
  });
  test("rows list seed, state, the take when there is one, and the batch", () => {
    const rows = renderRows([
      { job_id: "j1", batch_id: "b1", seed: 7, state: "completed", track_id: "t7" },
      { job_id: "j2", batch_id: "b2", seed: 8, state: "queued", track_id: null },
    ]);
    expect(rows[0]).toEqual({ key: "j1", text: "seed 7 · done", trackHref: "/tracks/t7", batchHref: "/batches/b1" });
    expect(rows[1]).toEqual({ key: "j2", text: "seed 8 · queued", trackHref: null, batchHref: "/batches/b2" });
    expect(renderRows(undefined)).toEqual([]);
  });
  test("plan jobs link to the Desk scoped by the job", () => {
    expect(planJobsHref("j 1")).toBe("/desk?job_id=j%201");
  });
  test("one completed plan job gets the plain label", () => {
    expect(planJobLabel(0, 1)).toBe("this job");
  });
  test("several completed plan jobs are numbered i of n", () => {
    expect([0, 1, 2].map((i) => planJobLabel(i, 3))).toEqual(["plan job 1 of 3", "plan job 2 of 3", "plan job 3 of 3"]);
  });
  test("no completed plan jobs: the template's each renders no link, so no label is asked for", () => {
    expect([].map((_, i) => planJobLabel(i, 0))).toEqual([]);
    expect(planJobLabel(0, 0)).toBe("this job");
  });
  test("a closed plan capability in an estimate refusal is said plainly", () => {
    expect(planSeedsRefusalText(503, "GENERATION_DISABLED", "x", { reason: "worker_outdated", missing: ["plan", "v2"] })).toMatch(/not available.*plan, v2/i);
    expect(planSeedsRefusalText(422, "INVALID_ITEM", "bad", { rule: "plan_requires_no_abc" })).toContain("INVALID_ITEM");
  });
});

describe("keepRequest", () => {
  const p = { abc: "X:1\nK:C\nCDEF|]\n", title: "A tune", base: { title: "Base" }, score: { dialect: "yue2" as const } };
  test("a score_render of the plan's own text in its dialect, defaults for everything else", () => {
    expect(keepRequest(p)).toEqual({
      kind: "score_render",
      title: "A tune",
      score_render: { score: { abc: p.abc }, dialect: "yue2", soundfont: null, bpm: null, tail_s: null, gain: null, chords: null },
    });
  });
  test("the base title names an untitled plan; a plan with no text has no request", () => {
    expect(keepRequest({ ...p, title: "" })?.title).toBe("Base");
    expect(keepRequest({ ...p, title: "", base: { title: "" } })?.title).toBeNull();
    expect(keepRequest({ ...p, abc: " " })).toBeNull();
    expect(keepRequest({ ...p, abc: undefined })).toBeNull();
  });
});
