import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import { audienceLabel, groupActions, hintLink, moreText, type HintLink } from "./nextActionsView";

type Action = components["schemas"]["NextAction"];
type Page = components["schemas"]["NextActionsPage"];

const act = (code: string, over: Partial<Action> = {}): Action =>
  ({ code, priority: 50, audience: "both", evidence: {}, subject: { kind: "library", id: null }, text: `${code} text`, ...over }) as Action;
const page = (items: Action[], groups: Page["groups"] = []): Page => ({ items, groups, total: items.length, next_cursor: null, generated_at: "2026-11-01T00:00:00Z" });

describe("groupActions", () => {
  test("groups by code in first-seen order and attaches the more count", () => {
    const g = groupActions(
      page([act("a", { text: "1" }), act("b"), act("a", { text: "2" })], [{ code: "a", more: 3 }, { code: "z", more: 9 }]),
    );
    expect(g.map((x) => x.code)).toEqual(["a", "b"]);
    expect(g[0]?.items.map((i) => i.text)).toEqual(["1", "2"]);
    expect(g[0]?.more).toBe(3);
    expect(g[1]?.more).toBe(0);
  });
  test("empty page and a null page give no groups", () => {
    expect(groupActions(page([]))).toEqual([]);
    expect(groupActions(null)).toEqual([]);
  });
  test("more text", () => {
    expect(moreText(3)).toBe("3 more like this");
    expect(moreText(0)).toBe("");
  });
});

describe("audienceLabel", () => {
  test("only human-audience items read needs you", () => {
    expect(audienceLabel(act("a", { audience: "human" }))).toBe("needs you");
    expect(audienceLabel(act("a", { audience: "both" }))).toBe("");
    expect(audienceLabel(act("a", { audience: "agent" }))).toBe("");
  });
});

const hint = (op: string, args: Record<string, unknown>, over: Partial<Action> = {}) => act("x", { hint: { op, args }, ...over });
const href = (l: HintLink | null) => (l && l.kind === "href" ? l.href : null);

describe("hintLink", () => {
  test("every hint op of C145.5", () => {
    expect(href(hintLink(hint("get_muse", { id: "m1" })))).toBe("/muses/m1");
    expect(href(hintLink(hint("get_song", { id: "s1" })))).toBe("/songs/s1");
    expect(href(hintLink(hint("get_record", { id: "r1", include: ["changes"] })))).toBe("/records/r1");
    expect(href(hintLink(hint("suggest_sequence", { record_id: "r1" })))).toBe("/records/r1?sequence=arc");
    expect(href(hintLink(hint("suggest_sequence", { record_id: "r1", strategy: "ramp_up" })))).toBe("/records/r1?sequence=arc");
    expect(href(hintLink(hint("list_songs", { muse_id: "m1", min_stars: 4, in_record: false })))).toBe("/board?muse=m1");
    expect(href(hintLink(hint("create_duel", { a_track_id: "a", b_track_id: "b", blind: true }, { subject: { kind: "song", id: "s9" } })))).toBe("/songs/s9");
    expect(href(hintLink(hint("propose_taxonomy", {})))).toBe("/sounds");
    expect(href(hintLink(hint("get_sound_taxonomy", { unmapped: true })))).toBe("/sounds");
  });
  test("ids are path-encoded", () => {
    expect(href(hintLink(hint("get_muse", { id: "a/b c" })))).toBe("/muses/a%2Fb%20c");
  });
  test("estimate_generation from_draft is a drafts intent to /human/generate, never an /api/ call", () => {
    const l = hintLink(hint("estimate_generation", { items: [{ mode: "from_draft", draft_ids: ["d1", "d2"] }] }));
    expect(l).toEqual({ kind: "intent", href: "/human/generate", intent: { mode: "from_draft", draft_ids: ["d1", "d2"] } });
    expect(l && "href" in l ? l.href.startsWith("/api/") : false).toBe(false);
  });
  test("estimate_generation with another mode or no drafts is not linked", () => {
    expect(hintLink(hint("estimate_generation", { items: [{ mode: "compose_new" }] }))).toBeNull();
    expect(hintLink(hint("estimate_generation", { items: [{ mode: "from_draft", draft_ids: [] }] }))).toBeNull();
    expect(hintLink(hint("estimate_generation", {}))).toBeNull();
  });
  test("an unknown op or missing args falls back to the subject page, else null", () => {
    expect(hintLink(hint("get_muse", {}))).toBeNull();
    expect(href(hintLink(hint("something_else", {}, { subject: { kind: "muse", id: "m2" } })))).toBe("/muses/m2");
    expect(hintLink(hint("something_else", {}))).toBeNull();
    expect(hintLink(hint("list_songs", { min_stars: 4 }))).toBeNull();
  });
  test("no hint: the subject page; taxonomy goes to the Proposals tab", () => {
    expect(href(hintLink(act("record_proposed_waiting", { subject: { kind: "record", id: "r7" } })))).toBe("/records/r7");
    expect(href(hintLink(act("taxonomy_proposals_open", { subject: { kind: "taxonomy", id: null } })))).toBe("/sounds?tab=proposals");
    expect(hintLink(act("features_missing"))).toBeNull();
  });
});
