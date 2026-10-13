import { describe, expect, test } from "bun:test";
import { INTENT_KEY, MAX_TAKES, clearIntent, intentToEstimate, loadIntent, saveIntent, validateIntent, type GenerationIntent } from "./genIntent";

function store() {
  const m = new Map<string, string>();
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k), m };
}
const ids = (n: number) => Array.from({ length: n }, (_, i) => `d${i}`);

describe("genIntent", () => {
  test("round trip through sessionStorage", () => {
    const s = store();
    const intents: GenerationIntent[] = [
      { mode: "new_seed", parent_track_id: "t1", n_seeds: 3 },
      { mode: "from_draft", draft_ids: ids(5) },
      { mode: "from_template", parent_track_ids: ["a", "b"], template_mode: "style_edit", patch: { style: "x" } },
      { mode: "compose_new", recipe: { id: "r", style: "s", lyrics: "l", cot: "c", seed: 1 }, title: "T" },
    ];
    for (const i of intents) {
      saveIntent(s, i);
      expect(s.m.has(INTENT_KEY)).toBe(true);
      expect(loadIntent(s)).toEqual(i);
    }
    clearIntent(s);
    expect(loadIntent(s)).toBeNull();
  });
  test("garbage in storage is ignored", () => {
    const s = store();
    s.setItem(INTENT_KEY, "{nope");
    expect(loadIntent(s)).toBeNull();
    s.setItem(INTENT_KEY, JSON.stringify({ mode: "bogus" }));
    expect(loadIntent(s)).toBeNull();
  });
  test("51 drafts refused, 50 ok", () => {
    expect(MAX_TAKES).toBe(50);
    expect(validateIntent({ mode: "from_draft", draft_ids: ids(51) })).toMatch(/batches of at most 50/);
    expect(validateIntent({ mode: "from_draft", draft_ids: ids(50) })).toBeNull();
    expect(validateIntent({ mode: "from_draft", draft_ids: [] })).not.toBeNull();
    expect(validateIntent({ mode: "from_template", parent_track_ids: ["a"], template_mode: "new_seed" })).not.toBeNull();
    expect(validateIntent({ mode: "from_template", parent_track_ids: ids(51), template_mode: "new_seed" })).toMatch(/at most 50/);
    expect(validateIntent({ mode: "new_seed", n_seeds: 51, parent_track_id: "t" })).toMatch(/at most 50/);
    expect(validateIntent({ mode: "new_seed", parent_track_id: "" })).not.toBeNull();
  });
  test("variant intent becomes an estimate_variant call", () => {
    const e = intentToEstimate({ mode: "new_seed", parent_track_id: "t1", n_seeds: 3 });
    expect(e).toEqual({ kind: "variant", trackId: "t1", body: { mode: "new_seed", n_seeds: 3 } });
    const e2 = intentToEstimate({ mode: "lyric_edit", parent_track_id: "t1", patch: { lyrics: "x" }, seeds: [1, 2], title: "T" });
    expect(e2).toEqual({ kind: "variant", trackId: "t1", body: { mode: "lyric_edit", patch: { lyrics: "x" }, seeds: [1, 2], title: "T" } });
  });
  test("multi-item intents become estimate_generation items", () => {
    expect(intentToEstimate({ mode: "from_draft", draft_ids: ["a", "b"] })).toEqual({ kind: "generation", body: { items: [{ mode: "from_draft", draft_ids: ["a", "b"] }] } });
    expect(intentToEstimate({ mode: "from_template", parent_track_ids: ["a", "b"], template_mode: "style_edit", patch: { style: "z" } })).toEqual({
      kind: "generation",
      body: { items: [{ mode: "from_template", parent_track_ids: ["a", "b"], template_mode: "style_edit", patch: { style: "z" } }] },
    });
    expect(intentToEstimate({ mode: "compose_new", recipe: { id: "r" }, title: "T" })).toEqual({
      kind: "generation",
      body: { items: [{ mode: "compose_new", recipe: { id: "r" } as never, title: "T" }] },
    });
  });
});

describe("parseSeeds", () => {
  test("lists, blanks and rejects", async () => {
    const { parseSeeds } = await import("./genIntent");
    expect(parseSeeds("")).toEqual([]);
    expect(parseSeeds("1, 2 3")).toEqual([1, 2, 3]);
    expect(parseSeeds("1,1")).toBeNull();
    expect(parseSeeds("-1")).toBeNull();
    expect(parseSeeds("a")).toBeNull();
    expect(parseSeeds("99999999999999999")).toBeNull();
  });
});

describe("entry-point intents (C84.2)", () => {
  test("backlog selection round trips and caps at 50", async () => {
    const { draftsIntent } = await import("./genIntent");
    const s = store();
    for (const n of [1, 7, 50]) {
      const i = draftsIntent(ids(n));
      expect(validateIntent(i)).toBeNull();
      saveIntent(s, i);
      expect(loadIntent(s)).toEqual(i);
      expect(intentToEstimate(i)).toEqual({ kind: "generation", body: { items: [{ mode: "from_draft", draft_ids: ids(n) }] } });
    }
    expect(validateIntent(draftsIntent(ids(51)))).toMatch(/batches of at most 50/);
    expect(validateIntent(draftsIntent([]))).not.toBeNull();
  });
  test("library template selection is 2..50 and round trips", async () => {
    const { templateIntent } = await import("./genIntent");
    const s = store();
    for (const n of [2, 3, 50]) {
      const i = templateIntent(ids(n), "style_edit");
      expect(validateIntent(i)).toBeNull();
      saveIntent(s, i);
      expect(loadIntent(s)).toEqual(i);
    }
    expect(validateIntent(templateIntent(ids(1), "style_edit"))).not.toBeNull();
    expect(validateIntent(templateIntent(ids(51), "style_edit"))).toMatch(/at most 50/);
  });
  test("variant and compose intents round trip", async () => {
    const { variantIntent, composeIntent } = await import("./genIntent");
    const s = store();
    const v = variantIntent("t1", "new_seed");
    expect(v).toEqual({ mode: "new_seed", parent_track_id: "t1", n_seeds: 3 });
    saveIntent(s, v);
    expect(loadIntent(s)).toEqual(v);
    expect(variantIntent("t1", "lyric_edit")).toEqual({ mode: "lyric_edit", parent_track_id: "t1" });
    const c = composeIntent();
    saveIntent(s, c);
    expect(loadIntent(s)).toEqual(c);
    expect(c.mode).toBe("compose_new");
  });
});

describe("canGenerate (C84)", () => {
  test("only humans or principals with the generate scope, and only on known lossless recipes", async () => {
    const { canGenerate } = await import("./genIntent");
    const human = { kind: "human" as const, scopes: ["read", "write", "generate", "admin"] };
    expect(canGenerate(human, { recipe_known: true, lossy: false })).toBe(true);
    expect(canGenerate(human, { recipe_known: false, lossy: false })).toBe(false);
    expect(canGenerate(human, { recipe_known: true, lossy: true })).toBe(false);
    expect(canGenerate(human, { recipe_known: false, lossy: true })).toBe(false);
    expect(canGenerate({ kind: "device", scopes: ["read", "write", "generate"] }, { recipe_known: true, lossy: false })).toBe(false);
    expect(canGenerate({ kind: "human", scopes: ["read", "write"] }, { recipe_known: true, lossy: false })).toBe(false);
    expect(canGenerate({ kind: "agent", scopes: ["generate"] }, { recipe_known: true, lossy: false })).toBe(true);
    expect(canGenerate(null, { recipe_known: true, lossy: false })).toBe(false);
  });
});
