import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import type { createSession } from "../api/session";
import { getCallback, listCallbacks, submitVerdict, VERDICTS } from "./callbacksApi";
import { getListenHeat } from "./heatApi";
import { suggestPairs } from "./pairSuggestApi";
import { APP_ROUTES, matchRoute } from "./router";
import { getPatch, listPatches, proposePatch, renderIntent } from "./songPrApi";
import { createVoiceNote } from "./voiceApi";
import { CONTEXTUAL_ROUTES, NAV_LINKS, isTabRoot, subActive, subLinks, tabFor, titleFor } from "./nav";
import { RECEIPT_GAP_MS } from "./receipts";

function fakeApi(handler: (req: Request) => Response) {
  const seen: Request[] = [];
  const api = createApi({
    baseUrl: "http://x.test",
    fetch: (req) => {
      seen.push(req.clone());
      return Promise.resolve(handler(req));
    },
    session: { ok() {}, confirmed() {}, networkFailure: () => Promise.resolve(), retryDelay: () => Promise.resolve() } as unknown as ReturnType<typeof createSession>,
  });
  return { api, seen };
}
const callbacksPath = (r: Request) => `${r.method} ${new URL(r.url).pathname}`;
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
const problem = (status: number, code: string) => json(status, { code, message: code, details: {} });

describe("M5 routes and nav (C262)", () => {
  test("the patch, callbacks and receipts routes exist and sit in their tabs", () => {
    expect(matchRoute(APP_ROUTES, "/patches/p1")?.params).toEqual({ id: "p1" });
    expect(matchRoute(APP_ROUTES, "/judge/callbacks")?.route.name).toBe("callbacks");
    expect(matchRoute(APP_ROUTES, "/judge/callbacks/c1")?.params).toEqual({ id: "c1" });
    expect(matchRoute(APP_ROUTES, "/receipts")?.route.name).toBe("receipts");
    expect(tabFor("patch")).toBe("library");
    expect(tabFor("callbacks")).toBe("judge");
    expect(tabFor("callback")).toBe("judge");
    expect(tabFor("receipts")).toBe("more");
  });
  test("Callbacks is a Judge sub-view; Receipts a More row; the ids are contextual", () => {
    expect(subLinks("judge").map((l) => l.label)).toEqual(["One by one", "Callbacks", "Duels", "Ladder"]);
    const link = NAV_LINKS.find((l) => l.path === "/judge/callbacks")!;
    expect(subActive(link, "callbacks")).toBe(true);
    expect(subActive(link, "callback")).toBe(true);
    expect(subActive(link, "duels")).toBe(false);
    expect(isTabRoot("callbacks")).toBe(true);
    expect(isTabRoot("callback")).toBe(false);
    expect(NAV_LINKS.find((l) => l.path === "/receipts")).toMatchObject({ label: "Receipts", tab: "more" });
    for (const n of ["patch", "callback"]) expect(CONTEXTUAL_ROUTES.has(n), n).toBe(true);
    for (const [n, t] of [["patch", "Song PR"], ["callbacks", "Callbacks"], ["callback", "Callback"], ["receipts", "Receipts"]] as const) expect(titleFor(n)).toBe(t);
  });
  test("a receipt group ends after 30 minutes of quiet", () => {
    expect(RECEIPT_GAP_MS).toBe(1_800_000);
  });
});

describe("M5 api wrappers", () => {
  test("proposePatch sends the key and the body, and returns the patch", async () => {
    const { api, seen } = fakeApi(() => json(201, { id: "p1" }));
    const r = await proposePatch(api, "k1", { baseTrackId: "t", addressesCommentIds: ["c"], rationale: "r", ops: [{ op: "set_style", style: "s" }] });
    expect(r.ok).toBe(true);
    expect(seen[0]!.headers.get("Idempotency-Key")).toBe("k1");
    expect(new URL(seen[0]!.url).pathname).toBe("/api/patches");
    expect(await seen[0]!.json()).toEqual({ base_track_id: "t", addresses_comment_ids: ["c"], rationale: "r", ops: [{ op: "set_style", style: "s" }] });
  });
  test("a stub answer becomes a Failure with its code", async () => {
    const { api } = fakeApi(() => problem(501, "NOT_IMPLEMENTED"));
    const r = await getPatch(api, "p1");
    expect(r).toMatchObject({ ok: false, status: 501, code: "NOT_IMPLEMENTED" });
    expect(await listPatches(api, { limit: 5 })).toMatchObject({ ok: false, status: 501 });
  });
  test("the callback wrappers hit the frozen paths", async () => {
    const { api, seen } = fakeApi(() => json(200, { items: [], total: 0, next_cursor: null }));
    await listCallbacks(api, { state: ["pending"], limit: 10 });
    await getCallback(api, "c1");
    await submitVerdict(api, "c1", "not_yet", "n", 12.5);
    expect(seen.map((s) => callbacksPath(s))).toEqual(["GET /api/callbacks", "GET /api/callbacks/c1", "POST /api/callbacks/c1/verdicts"]);
    expect(new URL(seen[0]!.url).search).toBe("?state=pending&limit=10");
    expect(await seen[2]!.json()).toEqual({ verdict: "not_yet", note: "n", heard_at_s: 12.5 });
    expect(VERDICTS.fix).toEqual(["fixed", "not_yet", "worse"]);
    expect(VERDICTS.keep).toEqual(["kept", "lost"]);
  });
  test("heat, pair suggestions and voice notes hit the frozen paths", async () => {
    const { api, seen } = fakeApi(() => json(200, {}));
    await getListenHeat(api, "t1");
    await suggestPairs(api, { scope: "global", k: 3 });
    await createVoiceNote(api, "t1", "k2", { audioBase64: "AAAA", atSecond: 1.5, kind: "fix" });
    expect(seen.map((s) => callbacksPath(s))).toEqual(["GET /api/tracks/t1/heat", "GET /api/duels/suggestions", "POST /api/tracks/t1/voice-notes"]);
    expect(await seen[2]!.json()).toEqual({ audio_base64: "AAAA", at_second: 1.5, kind: "fix" });
    expect(seen[2]!.headers.get("Idempotency-Key")).toBe("k2");
  });
  test("the render button hands the M3 confirm flow a song_pr intent", () => {
    expect(renderIntent("p1", 2)).toEqual({ mode: "song_pr", patch_id: "p1", n_seeds: 2 });
  });
});
