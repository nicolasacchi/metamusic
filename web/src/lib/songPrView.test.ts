import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import type { createSession } from "../api/session";
import type { components } from "../api/schema";
import { intentToEstimate, loadIntent, validateIntent } from "./genIntent";
import {
  addressable,
  boundsUsage,
  buildOps,
  callbackTally,
  draftProblem,
  loadPatchList,
  loadPatch,
  renderCount,
  spotText,
  styleTermDelta,
  toLoad,
  type PatchDraft,
} from "./songPrView";
import { proposePatch, renderIntent } from "./songPrApi";

type Comment = components["schemas"]["Comment"];
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
function fakeApi(handler: (req: Request) => Response | Promise<Response>) {
  return createApi({
    baseUrl: "http://x.test",
    fetch: (req) => Promise.resolve(handler(req)),
    session: { ok() {}, confirmed() {}, networkFailure: () => Promise.resolve(), retryDelay: () => Promise.resolve() } as unknown as ReturnType<typeof createSession>,
  });
}
const comment = (o: Partial<Comment>): Comment =>
  ({ id: "c", track_id: "t", kind: "fix", state: "open", hidden: false, at_second: 72, section: { label: "chorus 1", confidence: 0.7, source: "plan" }, body: "b", ...o }) as Comment;

describe("addressable comments", () => {
  test("only visible fix comments that are open or regressed", () => {
    const all = [
      comment({ id: "a" }),
      comment({ id: "b", state: "regressed" }),
      comment({ id: "c", state: "addressed" }),
      comment({ id: "d", state: "resolved" }),
      comment({ id: "e", kind: "note" }),
      comment({ id: "f", hidden: true }),
    ];
    expect(addressable(all).map((c) => c.id)).toEqual(["a", "b"]);
  });
  test("a spot reads time and section, and says so when there is none", () => {
    expect(spotText(comment({}))).toBe("1:12 · chorus 1 · ~70%");
    expect(spotText(comment({ section: null }))).toBe("1:12");
    expect(spotText(comment({ at_second: null, section: null }))).toBe("no time");
  });
});

describe("patch draft", () => {
  const base: PatchDraft = {
    rationale: "tighten the hook",
    title: "",
    style: "pop, bright",
    baseStyle: "pop, bright",
    lines: [{ section_idx: 1, line_idx: 0, before: "old", text: "old" }],
    comments: ["a"],
  };
  test("an unchanged line and an unchanged style produce no ops", () => {
    expect(buildOps(base)).toEqual([]);
  });
  test("changed lines become replace_line, a changed style set_style", () => {
    const d = { ...base, style: "pop, dark", lines: [{ section_idx: 1, line_idx: 0, before: "old", text: " new " }] };
    expect(buildOps(d)).toEqual([
      { op: "replace_line", section_idx: 1, line_idx: 0, text: "new" },
      { op: "set_style", style: "pop, dark" },
    ]);
  });
  test("style term delta counts the larger side", () => {
    expect(styleTermDelta("pop, bright", "pop, bright")).toBe(0);
    expect(styleTermDelta("pop, bright", "pop, dark, slow")).toBe(2);
    expect(styleTermDelta("pop, bright, slow", "pop")).toBe(2);
  });
  test("usage compares with the frozen bounds", () => {
    const d = { ...base, style: "rock", lines: Array.from({ length: 9 }, (_, i) => ({ section_idx: 0, line_idx: i, before: "a", text: "b" })) };
    const u = boundsUsage(d);
    expect(u).toMatchObject({ lines: 9, maxLines: 8, styleTerms: 2, over: ["lines"] });
  });
  test("problems: nothing selected, no rationale, no change, a blank line, too long", () => {
    expect(draftProblem({ ...base, comments: [] })).toMatch(/comment/);
    expect(draftProblem({ ...base, rationale: "  " })).toMatch(/why/i);
    expect(draftProblem(base)).toMatch(/change/i);
    const ok = { ...base, lines: [{ section_idx: 1, line_idx: 0, before: "old", text: "new" }] };
    expect(draftProblem(ok)).toBeNull();
    expect(draftProblem({ ...ok, lines: [{ section_idx: 1, line_idx: 0, before: "old", text: "   " }] })).toMatch(/blank/);
    expect(draftProblem({ ...ok, lines: [{ section_idx: 1, line_idx: 0, before: "old", text: "x".repeat(201) }] })).toMatch(/200/);
    expect(draftProblem({ ...ok, lines: [{ section_idx: 1, line_idx: 0, before: "old", text: "a\nb" }] })).toMatch(/one line/);
    expect(draftProblem({ ...ok, lines: [{ section_idx: 1, line_idx: 0, before: "old", text: "[Verse]" }] })).toMatch(/tag/);
  });
  test("a draft over the line bound is refused before the server is asked", () => {
    const d = { ...base, lines: Array.from({ length: 9 }, (_, i) => ({ section_idx: 0, line_idx: i, before: "a", text: "b" })) };
    expect(draftProblem(d)).toMatch(/8 lines/);
  });
});

describe("patch facts", () => {
  test("render count is distinct non-cancelled batches", () => {
    const r = (batch_id: string, state: string) => ({ batch_id, job_id: batch_id + state, seed: 1, state, child_track_id: null });
    expect(renderCount([r("b1", "done"), r("b1", "running"), r("b2", "cancelled"), r("b3", "queued")])).toBe(2);
    expect(renderCount([])).toBe(0);
  });
  test("callback tally text", () => {
    const t = { total: 3, answered: 2, fixed: 1, not_yet: 1, worse: 0, kept: 0, lost: 0 };
    expect(callbackTally(t)).toBe("2 of 3 answered · 1 fixed · 1 not yet");
    expect(callbackTally({ ...t, total: 0, answered: 0, fixed: 0, not_yet: 0 })).toBe("no callbacks yet");
    expect(callbackTally({ ...t, answered: 3, fixed: 0, not_yet: 0, kept: 2, lost: 1 })).toBe("3 of 3 answered · 2 kept · 1 lost");
  });
});

describe("loading states", () => {
  test("501 and 503 are 'not available yet', other failures errors, success ready", () => {
    expect(toLoad({ ok: false, status: 501, code: "NOT_IMPLEMENTED", message: "m", stale: false })).toEqual({ state: "unavailable" });
    expect(toLoad({ ok: false, status: 503, code: "UNAVAILABLE", message: "m", stale: false })).toEqual({ state: "unavailable" });
    expect(toLoad({ ok: false, status: 404, code: "NOT_FOUND", message: "gone", stale: false })).toEqual({ state: "error", message: "gone" });
    expect(toLoad({ ok: true, value: 3 })).toEqual({ state: "ready", value: 3 });
  });
  test("a mocked 501 from list_patches and get_patch is the unavailable state", async () => {
    const api = fakeApi(() => json(501, { code: "NOT_IMPLEMENTED", message: "x", details: { task: "S1" } }));
    expect(await loadPatchList(api, "t1")).toEqual({ state: "unavailable" });
    expect(await loadPatch(api, "p1")).toEqual({ state: "unavailable" });
  });
  test("a mocked 503 is unavailable too", async () => {
    const api = fakeApi(() => json(503, { code: "UNAVAILABLE", message: "x", details: { component: "songpr" } }));
    expect(await loadPatchList(api, "t1")).toEqual({ state: "unavailable" });
  });
  test("a 200 list is ready and asks for this take's patches", async () => {
    let url = "";
    const api = fakeApi((r) => {
      url = r.url;
      return json(200, { items: [], total: 0, next_cursor: null });
    });
    expect(await loadPatchList(api, "t1")).toEqual({ state: "ready", value: { items: [], total: 0, next_cursor: null } });
    expect(new URL(url).searchParams.get("track_id")).toBe("t1");
  });
  test("a thrown network error is an error, never a crash", async () => {
    const api = fakeApi(() => {
      throw new TypeError("offline");
    });
    const r = await loadPatch(api, "p1");
    expect(r.state).toBe("error");
  });
});

describe("propose failures", () => {
  const input = { baseTrackId: "t", addressesCommentIds: ["c"], rationale: "r", ops: [{ op: "set_style" as const, style: "s" }] };
  test("a stub (501) or unwired (503) answer is unavailable, a bounds refusal an error with the server's words", async () => {
    const stub = await proposePatch(fakeApi(() => json(501, { code: "NOT_IMPLEMENTED", message: "x", details: {} })), "k", input);
    const unwired = await proposePatch(fakeApi(() => json(503, { code: "UNAVAILABLE", message: "x", details: {} })), "k", input);
    const bounds = await proposePatch(fakeApi(() => json(422, { code: "PATCH_OUT_OF_BOUNDS", message: "9 lines is over the bound of 8", details: {} })), "k", input);
    expect(toLoad(stub)).toEqual({ state: "unavailable" });
    expect(toLoad(unwired)).toEqual({ state: "unavailable" });
    expect(toLoad(bounds)).toEqual({ state: "error", message: "9 lines is over the bound of 8" });
  });
});

describe("render intent through the M3 confirm flow", () => {
  const store = () => {
    const m = new Map<string, string>();
    return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) };
  };
  test("a song_pr intent survives sessionStorage and prices as an estimate_generation item", () => {
    const s = store();
    const i = renderIntent("p1", 2);
    s.setItem("mm.genIntent", JSON.stringify(i));
    const back = loadIntent(s);
    expect(back).toEqual(i);
    expect(validateIntent(i)).toBeNull();
    expect(intentToEstimate(i)).toEqual({ kind: "generation", body: { items: [{ mode: "song_pr", patch_id: "p1", n_seeds: 2 }] } });
  });
  test("a song_pr intent without a patch id is malformed", () => {
    const s = store();
    s.setItem("mm.genIntent", JSON.stringify({ mode: "song_pr", n_seeds: 2 }));
    expect(loadIntent(s)).toBeNull();
  });
});
