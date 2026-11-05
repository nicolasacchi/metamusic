import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import type { createSession } from "../api/session";
import type { components } from "../api/schema";
import { answerButtons, answeredText, isUnavailable, loadWaiting, nextPending, roughSpot, spotText, waitingText } from "./callbackView";
import { detectSupport, MIME_PREFERENCE, uploadVoice, voiceStart, voiceStep, type VoiceState } from "./voiceRecord";
import { groupReceipts, loadReceipts, RECEIPT_GAP_MS } from "./receipts";
import { canSuggest, loadSuggestions, NO_SECTION, pairTitle, startPair, suggestQuery } from "./smartRound";

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
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
const problem = (status: number, code: string, message = code) => json(status, { code, message, details: {} });

describe("callback view", () => {
  test("buttons follow the kind", () => {
    expect(answerButtons("fix").map((b) => b.label)).toEqual(["Fixed", "Not yet", "Worse"]);
    expect(answerButtons("keep").map((b) => b.verdict)).toEqual(["kept", "lost"]);
  });
  test("spot text names the section, else flags proportion", () => {
    const spot = (label: string | null, basis: "section" | "proportional", c = 0.9) => ({ spot: { label, basis, child_at_s: 72, source_at_s: null, confidence: c } });
    expect(spotText(spot("Chorus 2", "section"))).toBe("Chorus 2 · 1:12");
    expect(spotText(spot(null, "proportional", 0.3))).toBe("1:12 · by proportion");
    expect(roughSpot(spot(null, "proportional", 0.3))).toBe(true);
    expect(roughSpot(spot("Verse 1", "section"))).toBe(false);
  });
  test("501 and 503 are unavailable, 409 is not", () => {
    expect([501, 503, 409, 500].map((status) => isUnavailable({ status }))).toEqual([true, true, false, false]);
  });
  test("waiting count text and the answered text", () => {
    expect(waitingText(1)).toBe("1 callback waiting");
    expect(waitingText(3)).toBe("3 callbacks waiting");
    expect(answeredText({ replayed: true, verdict: { verdict: "fixed" }, regression_comment_id: null })).toBe("Answered: fixed (already recorded)");
    expect(answeredText({ replayed: false, verdict: { verdict: "lost" }, regression_comment_id: "c" })).toContain("fix note");
  });
  test("nextPending moves on in list order and wraps", () => {
    const l = [{ id: "a", state: "pending" as const }, { id: "b", state: "answered" as const }, { id: "c", state: "pending" as const }];
    expect(nextPending(l, "a")).toBe("c");
    expect(nextPending(l, "c")).toBe("a");
    expect(nextPending([{ id: "a", state: "pending" as const }], "a")).toBeNull();
  });
  test("loadWaiting reads the total and maps stubs to unavailable", async () => {
    const ok = fakeApi(() => json(200, { items: [], total: 4, next_cursor: null }));
    expect(await loadWaiting(ok.api)).toEqual({ kind: "count", n: 4 });
    expect(new URL(ok.seen[0]!.url).search).toBe("?state=pending&limit=1");
    expect(await loadWaiting(fakeApi(() => problem(501, "NOT_IMPLEMENTED")).api)).toEqual({ kind: "unavailable" });
    expect(await loadWaiting(fakeApi(() => problem(503, "UNAVAILABLE")).api)).toEqual({ kind: "unavailable" });
    expect(await loadWaiting(fakeApi(() => problem(500, "INTERNAL")).api)).toEqual({ kind: "error" });
    expect(await loadWaiting(fakeApi(() => Promise.reject(new Error("net")) as never).api)).toEqual({ kind: "error" });
  });
});

describe("voice recorder", () => {
  const rec = (types: string[]) => ({ MediaRecorder: { isTypeSupported: (m: string) => types.includes(m) }, mediaDevices: { getUserMedia() {} }, secure: true });
  test("feature check picks the best accepted container", () => {
    expect(detectSupport(rec([...MIME_PREFERENCE]))).toEqual({ ok: true, mime: "audio/webm;codecs=opus" });
    expect(detectSupport(rec(["audio/mp4"]))).toEqual({ ok: true, mime: "audio/mp4" }); // iOS Safari
  });
  test("it reports why recording is impossible", () => {
    expect(detectSupport({ ...rec([]), MediaRecorder: undefined })).toMatchObject({ ok: false });
    expect(detectSupport({ MediaRecorder: rec([]).MediaRecorder, secure: true })).toMatchObject({ ok: false });
    expect(detectSupport({ ...rec([...MIME_PREFERENCE]), secure: false })).toMatchObject({ ok: false });
    expect(detectSupport(rec(["audio/x-unknown"]))).toMatchObject({ ok: false });
    expect(detectSupport({ MediaRecorder: {}, mediaDevices: { getUserMedia() {} }, secure: true })).toEqual({ ok: true, mime: "" });
  });
  test("nothing reaches uploading without an explicit save", () => {
    let st: VoiceState = voiceStart({ ok: true, mime: "x" });
    expect(voiceStep(st, { e: "save" })).toEqual(st);
    st = voiceStep(st, { e: "start" });
    expect(voiceStep(st, { e: "save" })).toEqual(st);
    st = voiceStep(st, { e: "stop", seconds: 5, bytes: 1000 });
    expect(st.s).toBe("recorded"); // stopping never uploads
    expect(voiceStep(st, { e: "save" }).s).toBe("uploading");
    expect(voiceStep(voiceStep(st, { e: "discard" }), { e: "save" }).s).toBe("idle");
  });
  test("an oversized or overlong recording cannot be saved", () => {
    const big = voiceStep(voiceStep(voiceStart({ ok: true, mime: "x" }), { e: "start" }), { e: "stop", seconds: 5, bytes: 5 * 1024 * 1024 });
    expect(voiceStep(big, { e: "save" })).toEqual(big);
    const long = voiceStep(voiceStep(voiceStart({ ok: true, mime: "x" }), { e: "start" }), { e: "stop", seconds: 121, bytes: 10 });
    expect(voiceStep(long, { e: "save" })).toEqual(long);
  });
  test("an unsupported browser starts unsupported and stays there", () => {
    const st = voiceStart({ ok: false, reason: "no" });
    expect(voiceStep(st, { e: "start" })).toEqual(st);
  });
  test("501/503 on upload become the not-available state; saved returns to a fresh start", async () => {
    const blob = new Blob([new Uint8Array([1, 2, 3])]);
    for (const status of [501, 503]) {
      const o = await uploadVoice(fakeApi(() => problem(status, "X")).api, "t1", "k", blob, {});
      expect(o).toEqual({ kind: "unavailable" });
      const up = voiceStep(voiceStep(voiceStep(voiceStart({ ok: true, mime: "x" }), { e: "start" }), { e: "stop", seconds: 1, bytes: 3 }), { e: "save" });
      expect(voiceStep(up, { e: "failed", outcome: { kind: "unavailable" } })).toEqual({ s: "unavailable" });
    }
    const ok = fakeApi(() => json(201, { id: "c1" }));
    const saved = await uploadVoice(ok.api, "t1", "key9", blob, { atSecond: 2.5, kind: "fix" });
    expect(saved.kind).toBe("saved");
    expect(ok.seen).toHaveLength(1);
    expect(await ok.seen[0]!.json()).toEqual({ audio_base64: "AQID", at_second: 2.5, kind: "fix" });
    expect(ok.seen[0]!.headers.get("Idempotency-Key")).toBe("key9");
  });
  test("403 and 422 say what went wrong", async () => {
    const blob = new Blob([new Uint8Array([1])]);
    const h = await uploadVoice(fakeApi(() => problem(403, "HUMAN_ONLY")).api, "t", "k", blob, {});
    expect(h).toMatchObject({ kind: "error", status: 403 });
    const v = await uploadVoice(fakeApi(() => problem(422, "VOICE_INVALID", "reason: container")).api, "t", "k", blob, {});
    expect(v).toMatchObject({ kind: "error", message: "This browser's recording format is not accepted." });
  });
});

type Entry = components["schemas"]["AuditEntry"];
type Batch = components["schemas"]["BatchSummary"];
const P = (id: string) => ({ id, kind: id.split(":")[0]!, name: id });
const at = (min: number) => new Date(Date.UTC(2026, 10, 4, 10, min)).toISOString();
const entry = (id: string, who: string, min: number, op = "add_comment", status = 201, created: string[] = []): Entry =>
  ({ id, at: at(min), principal: P(who), op, status, created_ids: created, method: "POST", client: "", args_sha256: "", duration_ms: 1, error_code: null, idempotency: "", transport: "rest" }) as Entry;
const batch = (id: string, who: string, min: number, settled: number): Batch => ({ id, created_at: at(min), created_by: P(who), cost: { settled_micro_usd: settled } }) as unknown as Batch;

describe("receipts grouping", () => {
  test("a new group starts after a 30 minute gap, per principal", () => {
    const rows = [entry("1", "human:me", 0), entry("2", "human:me", 29), entry("3", "human:me", 60), entry("4", "agent:a", 1)];
    const g = groupReceipts(rows, []);
    expect(g.filter((x) => x.principal.id === "human:me").map((x) => x.entries.length).sort()).toEqual([1, 2]);
    expect(g).toHaveLength(3);
    expect(RECEIPT_GAP_MS).toBe(1_800_000);
  });
  test("exactly the gap stays in one group", () => {
    expect(groupReceipts([entry("1", "human:me", 0), entry("2", "human:me", 30)], [])).toHaveLength(1);
    expect(groupReceipts([entry("1", "human:me", 0), entry("2", "human:me", 31)], [])).toHaveLength(2);
  });
  test("counts ops, created ids and failures; newest first", () => {
    const g = groupReceipts([entry("1", "human:me", 0, "a", 201, ["x", "y"]), entry("2", "human:me", 1, "b", 409), entry("3", "human:me", 2, "a")], []);
    expect(g[0]).toMatchObject({ created: 2, failed: 1, ops: [{ op: "a", count: 2 }, { op: "b", count: 1 }] });
    const two = groupReceipts([entry("1", "human:me", 0), entry("2", "agent:a", 90)], []);
    expect(two.map((x) => x.principal.id)).toEqual(["agent:a", "human:me"]);
  });
  test("money lands in the creator's group; an orphan batch keeps its spend", () => {
    const g = groupReceipts([entry("1", "human:me", 0), entry("2", "agent:a", 0)], [batch("b1", "human:me", 5, 1_500_000), batch("b2", "human:me", 5, 500_000), batch("b3", "agent:z", 5, 7)]);
    expect(g.find((x) => x.principal.id === "human:me")).toMatchObject({ spendMicro: 2_000_000, batchIds: ["b1", "b2"] });
    expect(g.find((x) => x.principal.id === "agent:a")!.spendMicro).toBe(0);
    expect(g.find((x) => x.principal.id === "agent:z")).toMatchObject({ spendMicro: 7, entries: [] });
  });
  test("loadReceipts follows cursors and survives a failing batch list", async () => {
    let n = 0;
    const { api, seen } = fakeApi((r) => {
      const p = new URL(r.url).pathname;
      if (p === "/api/audit") return json(200, { items: [entry(`e${++n}`, "human:me", n)], next_cursor: n < 2 ? "c" : null });
      return problem(503, "UNAVAILABLE");
    });
    const r = await loadReceipts(api);
    expect(r).toMatchObject({ ok: true, value: { moneyMissing: true, truncated: false } });
    expect(seen.filter((s) => new URL(s.url).pathname === "/api/audit")).toHaveLength(2);
    const bad = await loadReceipts(fakeApi(() => problem(501, "NOT_IMPLEMENTED")).api);
    expect(bad).toMatchObject({ ok: false, status: 501 });
  });
});

describe("smart round", () => {
  const scope = { scope: "lineage" as const, rootTrackId: " r1 ", k: 99, section: NO_SECTION };
  test("the query clamps k and carries the section", () => {
    expect(suggestQuery(scope)).toEqual({ scope: "lineage", root_track_id: "r1", k: 15 });
    expect(suggestQuery({ ...scope, scope: "global", section: { kind: "chorus", ordinal: 2 } })).toEqual({ scope: "global", k: 15, section_kind: "chorus", section_ordinal: 2 });
    expect(canSuggest({ ...scope, rootTrackId: " " })).toBe(false);
    expect(canSuggest({ ...scope, scope: "global", rootTrackId: "" })).toBe(true);
  });
  test("a blind round never shows the titles", () => {
    const p = { a_title: "Secret A", b_title: "Secret B" };
    expect(pairTitle(p, 0, true)).toBe("Pair 1");
    expect(pairTitle(p, 0, true)).not.toContain("Secret");
    expect(pairTitle(p, 1, false)).toBe("Secret A vs Secret B");
  });
  test("suggestions: pairs, and 501/503 as unavailable", async () => {
    const ok = fakeApi(() => json(200, { scope: "global", pairs: [{ a_track_id: "a", b_track_id: "b", a_title: "A", b_title: "B", score: 0.1, p_a_beats_b: 0.5, sd_a: 1, sd_b: 1, human_verdicts: 0 }], model: { nu: 0.3, verdicts_counted: 9 }, root_track_id: null, section: null }));
    expect(await loadSuggestions(ok.api, { ...scope, scope: "global" })).toMatchObject({ kind: "pairs", verdictsCounted: 9 });
    expect(await loadSuggestions(fakeApi(() => problem(501, "NOT_IMPLEMENTED")).api, scope)).toEqual({ kind: "unavailable" });
    expect(await loadSuggestions(fakeApi(() => problem(503, "UNAVAILABLE")).api, scope)).toEqual({ kind: "unavailable" });
    expect(await loadSuggestions(fakeApi(() => problem(422, "SECTION_UNTIMED")).api, scope)).toMatchObject({ kind: "error", message: expect.stringContaining("timed") });
  });
  test("startPair sends a blind create_duel with the section lock and the key", async () => {
    const f = fakeApi(() => json(201, { id: "d1" }));
    expect(await startPair(f.api, { a_track_id: "a", b_track_id: "b" }, "k1", true, { kind: "chorus", ordinal: 1 })).toEqual({ kind: "created", id: "d1" });
    expect(await f.seen[0]!.json()).toEqual({ a_track_id: "a", b_track_id: "b", blind: true, section: { kind: "chorus", ordinal: 1 } });
    expect(f.seen[0]!.headers.get("Idempotency-Key")).toBe("k1");
    const plain = fakeApi(() => json(201, { id: "d2" }));
    await startPair(plain.api, { a_track_id: "a", b_track_id: "b" }, "k", true, NO_SECTION);
    expect(await plain.seen[0]!.json()).toEqual({ a_track_id: "a", b_track_id: "b", blind: true });
    expect(await startPair(fakeApi(() => problem(501, "NOT_IMPLEMENTED")).api, { a_track_id: "a", b_track_id: "b" }, "k", true, NO_SECTION)).toMatchObject({ kind: "error", status: 501 });
  });
});
