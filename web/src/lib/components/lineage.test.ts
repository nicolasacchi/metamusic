import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import GenerateButton from "./GenerateButton.svelte";
import ProvenancePanel from "./ProvenancePanel.svelte";
import LineageTree from "./LineageTree.svelte";
import TrackTable from "./TrackTable.svelte";
import Track from "../../routes/Track.svelte";
import Lineage from "../../routes/Lineage.svelte";
import { summaryKnown, summaryLossy, summaryReference, trackKnown, trackLossy, trackReference } from "../fixtures/m2c";
import type { components } from "../../api/schema";
import type { Viewer } from "../genIntent";

const html = (c: unknown, props: Record<string, unknown>) => render(c as never, { props } as never).body;
const human: Viewer = { kind: "human", scopes: ["read", "write", "generate", "admin"] };
const readOnly: Viewer = { kind: "human", scopes: ["read", "write"] };
const device: Viewer = { kind: "device", scopes: ["read", "write", "generate"] };

type Gen = NonNullable<components["schemas"]["TrackDetail"]["generation"]>;
const generation: Gen = {
  batch_id: "b1",
  job_id: "j1",
  operator: "reroll_seed",
  rationale: "try another seed",
  cost: { basis: "reconciled", display_micro_usd: 326217, display_usd: "0.326217" },
  env: { backend: "runpod", code_commit: "0123456789abcdef", decoder: "d", detail: { cuda: "12.8", gpu_name: "g", python: "3.12", torch: "2.10", worker_build: "w" }, engine: "yue2", env_id: "e", gpu_class: "A5000", image_digest: "sha256:abc", pkg_version: "1" , weights: {} },
  recipe_patch_json: null,
} as unknown as Gen;

const gb = (t: { recipe_known: boolean; lossy: boolean }, me: Viewer | null) => html(GenerateButton, { trackId: "t1", recipeKnown: t.recipe_known, lossy: t.lossy, me, go: () => {} });

describe("GenerateButton visibility", () => {
  test("humans with generate see the Variant menu on a known lossless track", () => {
    const h = gb(trackKnown, human);
    expect(h).toContain('data-testid="generate-button"');
    for (const l of ["New seed ×3", "Edit lyrics", "Edit style/BPM", "Same seed edit"]) expect(h).toContain(l);
  });
  test("hidden for devices, missing scope and unknown principal", () => {
    for (const me of [device, readOnly, null]) expect(gb(trackKnown, me)).not.toContain("generate-button");
  });
  test("hidden for the M2c reference and lossy fixtures", () => {
    expect(gb(trackReference, human)).not.toContain("generate-button");
    expect(gb(trackLossy, human)).not.toContain("generate-button");
    expect(trackReference.recipe_known).toBe(false);
    expect(trackLossy.lossy).toBe(true);
  });
});

describe("Track page generation entry points", () => {
  const page = (t: typeof trackKnown, viewer: Viewer | null) => html(Track, { id: t.id, initial: t, viewer });
  test("reference and lossy pages offer no generation button; known does for a human", () => {
    expect(page(trackKnown, human)).toContain('data-testid="generate-button"');
    expect(page(trackReference, human)).not.toContain("generate-button");
    expect(page(trackLossy, human)).not.toContain("generate-button");
    expect(page(trackKnown, device)).not.toContain("generate-button");
    expect(page(trackKnown, human)).toContain('data-testid="lineage-link"');
  });
  test("provenance appears only for generated takes", () => {
    expect(page(trackKnown, human)).not.toContain('data-testid="provenance"');
    const gen = { ...trackKnown, generation } as typeof trackKnown;
    expect(page(gen, human)).toContain('data-testid="provenance"');
  });
});

describe("ProvenancePanel", () => {
  const panel = (parentId: string | null, truncated: components["schemas"]["TrackDetail"]["truncated"] = null) =>
    html(ProvenancePanel, { trackId: "t1", generation, parentId, truncated });
  test("shows operator, rationale, cost with basis and environment", () => {
    const h = panel(null);
    expect(h).toContain("reroll seed");
    expect(h).toContain("try another seed");
    expect(h).toContain("$0.33");
    expect(h).toContain("reconciled");
    expect(h).toContain("yue2");
    expect(h).toContain("0123456");
    expect(h).toContain("/jobs/j1");
  });
  test("A/B with parent and Compare only for a child", () => {
    expect(panel(null)).not.toContain("A/B with parent");
    const h = panel("p1");
    expect(h).toContain("A/B with parent");
    expect(h).toContain("/compare?a=p1&amp;b=t1");
  });
  test("truncation badge", () => {
    expect(panel(null, { abc: false, semantic: true })).toContain("prov-truncated");
    expect(panel(null, { abc: false, semantic: false })).not.toContain("prov-truncated");
    expect(panel(null, null)).not.toContain("prov-truncated");
  });
});

type LNode = components["schemas"]["LineageNode"];
const node = (id: string, parent: string | null, generation: number, o: Partial<LNode> = {}): LNode => ({
  id,
  parent_id: parent,
  generation,
  title: "Song " + id,
  status: "candidate",
  seed: 1,
  recipe_known: true,
  lossy: false,
  request_id: "r-" + id,
  origin: "generated",
  operator: parent ? "reroll_seed" : null,
  inferred: false,
  rationale: null,
  stars: null,
  critic_stars: null,
  outcome: null,
  cost: null,
  job_id: null,
  created_at: "2026-10-14T10:00:00Z",
  children_count: 0,
  ...o,
});

describe("LineageTree", () => {
  const nodes = [node("root", null, 0, { children_count: 2 }), node("a", "root", 1, { stars: 4, inferred: true, cost: { basis: "estimate", display_micro_usd: 5800, display_usd: "0.0058" } }), node("ref", "root", 1, { recipe_known: false, seed: null, lossy: true })];
  test("rows, operator chips, inferred badge, cost and parent actions", () => {
    const h = html(LineageTree, { nodes, me: human });
    expect(h.match(/data-testid="lineage-node"/g)?.length).toBe(3);
    expect(h).toContain("reroll seed");
    expect(h).toContain('data-testid="lineage-inferred"');
    expect(h).toContain("$0.0058");
    expect(h.match(/lineage-ab-parent/g)?.length).toBe(2);
    expect(h).toContain("└─");
  });
  test("reference nodes offer no Variant; known nodes do", () => {
    const h = html(LineageTree, { nodes, me: human });
    expect(h.match(/data-testid="generate-button"/g)?.length).toBe(2);
    expect(html(LineageTree, { nodes, me: device })).not.toContain("generate-button");
    expect(html(LineageTree, { nodes, me: null })).not.toContain("generate-button");
  });
});

describe("Lineage page", () => {
  test("renders the count and the truncated badge", () => {
    const tree = { root_id: "root", total: 450, truncated: true, nodes: [node("root", null, 0)] };
    const h = html(Lineage, { id: "root", initial: tree, me: human });
    expect(h).toContain("1 of 450");
    expect(h).toContain("lineage-truncated");
    expect(h).toContain("lineage-more");
    const full = html(Lineage, { id: "root", initial: { ...tree, total: 1, truncated: false }, me: human });
    expect(full).not.toContain("lineage-truncated");
    expect(full).not.toContain("lineage-more");
  });
});

describe("TrackTable selection mode", () => {
  test("no checkbox column outside selection mode", () => {
    expect(html(TrackTable, { tracks: [summaryKnown], onplay: () => {} })).not.toContain("row-select");
  });
  test("reference and lossy rows are disabled with 'recipe unknown'", () => {
    const h = html(TrackTable, { tracks: [summaryKnown, summaryReference, summaryLossy], onplay: () => {}, selected: new Set([summaryKnown.id]) });
    const boxes = h.match(/<input[^>]*row-select[^>]*>/g) ?? [];
    expect(boxes.length).toBe(3);
    expect(boxes.filter((b) => b.includes("disabled")).length).toBe(2);
    expect(boxes[0]).toContain("checked");
    expect(h).toContain("recipe unknown");
  });
});
