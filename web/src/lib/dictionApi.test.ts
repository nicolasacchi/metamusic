import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import type { createSession } from "../api/session";
import { UNREACHABLE, calibrationText, recoverStage, getLexicon, getTrackDiction, heardText, lexiconQuery, rateText, recoverRecipe, setDictionMarks, updateLexiconEntry } from "./dictionApi";

function fake(handler: (req: Request) => Response) {
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

describe("dictionApi calls", () => {
  test("getTrackDiction sends the cursor only when there is one", async () => {
    const { api, seen } = fake(() => json(200, { track_id: "t1", lines: [] }));
    await getTrackDiction(api, "t1");
    await getTrackDiction(api, "t1", "abc");
    expect(new URL(seen[0]!.url).pathname).toBe("/api/tracks/t1/diction");
    expect(new URL(seen[0]!.url).search).toBe("");
    expect(new URL(seen[1]!.url).searchParams.get("cursor")).toBe("abc");
  });
  test("lists the server sent as null are lists", async () => {
    const { api } = fake(() => json(200, { track_id: "t1", lines: [{ section_idx: 0, line_idx: 0, words: null }], lexicon_hits: null }));
    const r = await getTrackDiction(api, "t1");
    expect(r.ok && r.value.lines[0]!.words).toEqual([]);
    expect(r.ok && r.value.lexicon_hits).toEqual([]);
    const lex = fake(() => json(200, { entries: [{ word: "pane", typical_heard: null, examples: null }], total: 1 }));
    const l = await getLexicon(lex.api, {});
    expect(l.ok && l.value.entries[0]!.examples).toEqual([]);
    expect(l.ok && l.value.entries[0]!.typical_heard).toEqual([]);
  });
  test("a refusal is a Failure with the server's words, and a network error is a Failure too", async () => {
    const a = fake(() => json(409, { code: "ANALYSIS_UNAVAILABLE", message: "no listening check" }));
    const r = await getTrackDiction(a.api, "t1");
    expect(r.ok).toBe(false);
    if (!r.ok) {
      expect(r.status).toBe(409);
      expect(r.message).toBe("no listening check");
    }
    const b = createApi({ baseUrl: "http://x.test", fetch: () => Promise.reject(new Error("down")), session: { ok() {}, confirmed() {}, networkFailure: () => Promise.resolve(), retryDelay: () => Promise.resolve() } as unknown as ReturnType<typeof createSession> });
    const n = await getTrackDiction(b, "t1");
    expect(n.ok).toBe(false);
    if (!n.ok) expect(n.code).toBe("NETWORK");
  });
  test("setDictionMarks PUTs the batch", async () => {
    const { api, seen } = fake(() => json(200, { track_id: "t1", marks: [] }));
    const r = await setDictionMarks(api, "t1", [{ section_idx: 0, line_idx: 1, mark: "unclear" }]);
    expect(r.ok).toBe(true);
    expect(seen[0]!.method).toBe("PUT");
    expect(new URL(seen[0]!.url).pathname).toBe("/api/tracks/t1/diction/marks");
    expect(await seen[0]!.json()).toEqual({ marks: [{ section_idx: 0, line_idx: 1, mark: "unclear" }] });
  });
  test("getLexicon sends the filters it was given", async () => {
    const { api, seen } = fake(() => json(200, { entries: [], total: 0 }));
    await getLexicon(api, { lang: "it", risk: true, q: "pan", cursor: "c" });
    const q = new URL(seen[0]!.url).searchParams;
    expect([q.get("lang"), q.get("risk"), q.get("q"), q.get("cursor"), q.get("limit")]).toEqual(["it", "true", "pan", "c", null]);
  });
  test("updateLexiconEntry PATCHes the folded word under its language", async () => {
    const { api, seen } = fake(() => json(200, { word: "pane", lang: "it" }));
    await updateLexiconEntry(api, "it", "pane", { status: "ignored" });
    expect(seen[0]!.method).toBe("PATCH");
    expect(new URL(seen[0]!.url).pathname).toBe("/api/diction/lexicon/it/pane");
    expect(await seen[0]!.json()).toEqual({ status: "ignored" });
  });
  test("recoverRecipe: one key per click, reused on retry; an empty style is left out", async () => {
    const { api, seen } = fake(() => json(201, { draft: { id: "rec-1" } }));
    const act = recoverRecipe(api, "ref1", { style: "  " });
    await act();
    await act();
    expect(seen[0]!.headers.get("Idempotency-Key")).toBe(seen[1]!.headers.get("Idempotency-Key"));
    expect(new URL(seen[0]!.url).pathname).toBe("/api/tracks/ref1/recover");
    expect(await seen[0]!.json()).toEqual({ cot: "melody" });
    const b = fake(() => json(201, { draft: { id: "rec-1" } }));
    await recoverRecipe(b.api, "ref1", { style: " italian pop " })();
    expect(await b.seen[0]!.json()).toEqual({ cot: "melody", style: "italian pop" });
  });
});

describe("lexicon words", () => {
  test("lexiconQuery drops empty filters", () => {
    expect(lexiconQuery({ lang: "", risk: "all", q: " " })).toEqual({});
    expect(lexiconQuery({ lang: "it", risk: "risky", q: " pan " })).toEqual({ lang: "it", risk: true, q: "pan" });
    expect(lexiconQuery({ lang: "", risk: "other", q: "" })).toEqual({ risk: false });
  });
  test("rateText and heardText", () => {
    expect(rateText({ attempts: 6, mangled: 4 })).toBe("4 of 6 mangled (67 %)");
    expect(rateText({ attempts: 0, mangled: 0 })).toBe("never heard");
    expect(heardText([{ text: "pane", n: 2 }, { text: "pene", n: 1 }])).toBe("pane (2), pene (1)");
    expect(heardText([])).toBe("");
  });
  test("calibrationText says how far the marking has come", () => {
    expect(calibrationText({ lines_marked: 0, tracks_marked: 0, clear: 0, unclear: 0, lang: "all" })).toBe("No lines marked yet. About 60 lines over 6 takes calibrate the thresholds.");
    expect(calibrationText({ lines_marked: 12, tracks_marked: 2, clear: 8, unclear: 4, lang: "it" })).toBe("12 lines marked on 2 takes (8 clear, 4 unclear) in it. Target: 60 lines over 6 takes.");
  });
});

describe("recoverStage", () => {
  const none = { tier: "0", flags: [] };
  const b = { tier: "B", flags: [] };
  test("waits for the dossier, then asks for the check when none is stored", () => {
    expect(recoverStage(null, null)).toBe("wait");
    expect(recoverStage(none, null)).toBe("analyze");
    expect(recoverStage({ tier: "A", flags: [] }, "")).toBe("analyze");
  });
  test("a stored check decides by its state", () => {
    expect(recoverStage(b, null)).toBe("wait");
    expect(recoverStage(b, "ok")).toBe("ready");
    expect(recoverStage(b, "missing")).toBe("analyze");
    expect(recoverStage({ tier: "A", flags: [{ code: "listening_check_failed" }] }, "error")).toBe("unavailable");
    expect(recoverStage(b, "skipped")).toBe("unavailable");
  });
  test("a running check shows progress, a failed read offers a retry", () => {
    expect(recoverStage(b, "pending")).toBe("pending");
    expect(recoverStage(b, UNREACHABLE)).toBe("retry");
  });
});
