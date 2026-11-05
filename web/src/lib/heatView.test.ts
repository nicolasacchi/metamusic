import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import type { createSession } from "../api/session";
import { binLevels, chipText, heatSummary, loadHeat, strips } from "./heatView";
import type { ListenHeat } from "./heatApi";

const heat = (o: Partial<ListenHeat> = {}): ListenHeat => ({
  track_id: "t", duration_s: 120, bins: 4, bin_s: 30, sessions: 2, plays: 3, completion_rate: 0.5,
  heard: [2, 1, 0, 0], replays: [0, 2, 0, 0], rewinds: [0, 1, 0, 0], skips: [0, 0, 0, 1], hotspots: [{ from_s: 30, to_s: 60, score: 5, label: "chorus 1" }], cold: [3], ...o,
});
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
function fakeApi(h: () => Response) {
  return createApi({
    baseUrl: "http://x.test",
    fetch: () => Promise.resolve(h()),
    session: { ok() {}, confirmed() {}, networkFailure: () => Promise.resolve(), retryDelay: () => Promise.resolve() } as unknown as ReturnType<typeof createSession>,
  });
}

describe("heat levels", () => {
  test("bins are scaled to the largest one", () => {
    expect(binLevels([2, 1, 0, 0])).toEqual([1, 0.5, 0, 0]);
    expect(binLevels([0, 0])).toEqual([0, 0]);
    expect(binLevels(null)).toEqual([]);
  });
  test("the strips pad a short array to the bin count and stay in 0..1", () => {
    const s = strips(heat({ heard: [4, 2], replays: null, rewinds: [1, 0, 0, 3], skips: [] }));
    expect(s.heard).toEqual([1, 0.5, 0, 0]);
    expect(s.hot).toEqual([0.333, 0, 0, 1].map((x) => Math.round(x * 1000) / 1000));
    expect(s.skips).toEqual([0, 0, 0, 0]);
  });
  test("hot is replays plus rewinds", () => {
    expect(strips(heat()).hot).toEqual([0, 1, 0, 0]);
  });
});

describe("hotspot chips and summary", () => {
  test("a chip names the span and its section", () => {
    expect(chipText({ from_s: 30, to_s: 60, score: 5, label: "chorus 1" })).toBe("0:30–1:00 · chorus 1");
    expect(chipText({ from_s: 75, to_s: 80, score: 2, label: null })).toBe("1:15–1:20");
  });
  test("summary for a screen reader, and for no listening at all", () => {
    expect(heatSummary(heat())).toBe("Listening heat: 2 sessions, 50% completed, 1 hotspot");
    expect(heatSummary(heat({ sessions: 0, hotspots: [], completion_rate: null }))).toBe("No listening recorded yet");
  });
});

describe("loadHeat", () => {
  test("501 and 503 are unavailable", async () => {
    expect(await loadHeat(fakeApi(() => json(501, { code: "NOT_IMPLEMENTED", message: "x", details: {} })), "t")).toEqual({ state: "unavailable" });
    expect(await loadHeat(fakeApi(() => json(503, { code: "UNAVAILABLE", message: "x", details: {} })), "t")).toEqual({ state: "unavailable" });
  });
  test("200 is ready", async () => {
    const r = await loadHeat(fakeApi(() => json(200, heat())), "t");
    expect(r.state).toBe("ready");
  });
  test("a network failure is an error", async () => {
    const api = fakeApi(() => {
      throw new TypeError("x");
    });
    expect((await loadHeat(api, "t")).state).toBe("error");
  });
});
