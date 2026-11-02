import { describe, expect, test } from "bun:test";
import {
  MAX_PENDING_REFETCH,
  MAX_STALE_RESTARTS,
  applySelect,
  axisPairError,
  boardHref,
  loadAtlas,
  readyOnce,
  recordHandOff,
  sequenceHref,
  sharedMuse,
  totalsText,
  visiblePoints,
  type AtlasGet,
  type AtlasPage,
  type AtlasQuery,
} from "./atlasApi";
import type { AtlasFilter, AtlasPoint } from "./atlasView";

const pt = (id: string, extra: Partial<AtlasPoint> = {}): AtlasPoint => ({ id, t: id, x: 0.5, y: 0.5, c: 0, st: 1, pk: 0, xb: "m", yb: "m", ...extra });
const axes = { x: { name: "tempo", kind: "numeric" as const }, y: { name: "energy", kind: "numeric" as const } };
const totals = { basis: { x: { d: 0, l: 0, m: 13, p: 0 }, y: { d: 0, l: 0, m: 13, p: 430 } }, hit: 443, missing: { x: 0, y: 0 }, plotted: 443, scope: 469, scope_total: 469, truncated: false };
const page = (points: AtlasPoint[], next: string | null, extra: Partial<AtlasPage> = {}): AtlasPage =>
  ({ axes, color_by: "none", def: "v1", generated_at: "", guides: [], layout_token: "t", legend: [], muses: [], next_cursor: next, pending: false, points, taxonomy_version: 1, totals, ...extra }) as AtlasPage;
const ok = (data: AtlasPage) => ({ data, response: { status: 200 } });
const stale = () => ({ error: { code: "ATLAS_STALE", message: "changed" }, response: { status: 409 } });
const noSleep = () => Promise.resolve();

describe("loadAtlas paging", () => {
  test("follows next_cursor to the end", async () => {
    const seen: (string | undefined)[] = [];
    const pages = [page([pt("a"), pt("b")], "c1"), page([pt("c")], "c2"), page([pt("d")], null)];
    const get: AtlasGet = async (q) => {
      seen.push(q.cursor);
      return ok(pages[seen.length - 1]!);
    };
    const r = await loadAtlas(get, {}, { sleep: noSleep });
    expect(r.kind).toBe("ok");
    if (r.kind !== "ok") return;
    expect(r.atlas.points.map((p) => p.id)).toEqual(["a", "b", "c", "d"]);
    expect(r.capped).toBe(false);
    expect(seen).toEqual([undefined, "c1", "c2"]);
  });
  test("stops at MAX_VIEW and says it was capped", async () => {
    let n = 0;
    const get: AtlasGet = async () => ok(page([pt("a" + n++), pt("b" + n++)], "more"));
    const r = await loadAtlas(get, {}, { maxView: 5, sleep: noSleep });
    if (r.kind !== "ok") throw new Error(r.kind);
    expect(r.atlas.points).toHaveLength(5);
    expect(r.capped).toBe(true);
    expect(n).toBe(6);
  });
  test("near_song_id is sent on the first page only", async () => {
    const qs: AtlasQuery[] = [];
    const get: AtlasGet = async (q) => {
      qs.push(q);
      return ok(qs.length === 1 ? page([pt("a")], "c") : page([pt("b")], null));
    };
    await loadAtlas(get, { near_song_id: "s1", x: "tempo" }, { sleep: noSleep });
    expect(qs[0]!.near_song_id).toBe("s1");
    expect(qs[1]!.near_song_id).toBeUndefined();
    expect(qs[1]!.x).toBe("tempo");
  });
  test("reports each page as it arrives", async () => {
    const seen: number[] = [];
    let i = 0;
    const get: AtlasGet = async () => ok(i++ === 0 ? page([pt("a")], "c") : page([pt("b")], null));
    await loadAtlas(get, {}, { sleep: noSleep, onProgress: (_a, n) => seen.push(n) });
    expect(seen).toEqual([1, 2]);
  });
  test("a failure other than stale is reported with the server's message", async () => {
    const get: AtlasGet = async () => ({ error: { code: "AXIS_PAIR_INVALID", message: "x and y must be two different axes" }, response: { status: 422 } });
    const r = await loadAtlas(get, {}, { sleep: noSleep });
    expect(r.kind).toBe("failed");
    if (r.kind === "failed") expect(r.failure.message).toBe("x and y must be two different axes");
  });
});

describe("loadAtlas stale restart", () => {
  test("a 409 ATLAS_STALE restarts from the first page after the debounce", async () => {
    const calls: (string | undefined)[] = [];
    const sleeps: number[] = [];
    const get: AtlasGet = async (q) => {
      calls.push(q.cursor);
      if (calls.length === 1) return ok(page([pt("a")], "c"));
      if (calls.length === 2) return stale();
      return ok(page([pt("z")], null));
    };
    const r = await loadAtlas(get, {}, { sleep: async (ms) => void sleeps.push(ms) });
    if (r.kind !== "ok") throw new Error(r.kind);
    expect(r.atlas.points.map((p) => p.id)).toEqual(["z"]);
    expect(calls).toEqual([undefined, "c", undefined]);
    expect(sleeps).toHaveLength(1);
    expect(sleeps[0]).toBeGreaterThan(0);
  });
  test("a repeating stale gives up", async () => {
    let n = 0;
    const get: AtlasGet = async () => (n++, stale());
    const r = await loadAtlas(get, {}, { sleep: noSleep });
    expect(r.kind).toBe("stale");
    expect(n).toBe(MAX_STALE_RESTARTS + 1);
  });
});

describe("loadAtlas pending refetch", () => {
  test("pending refetches after 1.5 s, at most three times", async () => {
    let n = 0;
    const sleeps: number[] = [];
    const get: AtlasGet = async () => (n++, ok(page([pt("a")], null, { pending: true })));
    const r = await loadAtlas(get, {}, { sleep: async (ms) => void sleeps.push(ms) });
    expect(n).toBe(1 + MAX_PENDING_REFETCH);
    expect(sleeps).toEqual([1500, 1500, 1500]);
    if (r.kind !== "ok") throw new Error(r.kind);
    expect(r.pendingLeft).toBe(true);
  });
  test("stops refetching once the layout is ready", async () => {
    let n = 0;
    const get: AtlasGet = async () => ok(page([pt("a")], null, { pending: n++ < 1 }));
    const r = await loadAtlas(get, {}, { sleep: noSleep });
    expect(n).toBe(2);
    if (r.kind === "ok") expect(r.pendingLeft).toBe(false);
  });
  test("a superseded load stops quietly", async () => {
    let n = 0;
    const get: AtlasGet = async () => (n++, ok(page([pt("a")], "c")));
    const r = await loadAtlas(get, {}, { sleep: noSleep, cancelled: () => n >= 1 });
    expect(r.kind).toBe("cancelled");
    expect(n).toBe(1);
  });
});

describe("helpers", () => {
  const f: AtlasFilter = { x: "", y: "", color: "", dim: false, muse: "", session: "s1", states: ["draft"], sounds: ["era:90s", "mood:chill"], q: "rain", crate: "cr", near: "n" };
  test("totals strip text", () => {
    expect(totalsText(totals)).toBe("443 plotted · 13 measured · 430 estimated · 26 not plotted");
    expect(totalsText({ ...totals, scope_total: 6000, truncated: true })).toContain("6000 match, first 469 used");
  });
  test("equal axes are refused with the server text; blanks are the defaults", () => {
    expect(axisPairError("energy", "energy")).toBe("x and y must be two different axes");
    expect(axisPairError("", "")).toBe("");
    expect(axisPairError("energy", "")).toBe("x and y must be two different axes");
    expect(axisPairError("tempo", "energy")).toBe("");
  });
  test("predicted points are hidden by default", () => {
    const ps = [pt("a"), pt("b", { yb: "p" })];
    expect(visiblePoints(ps, true).map((p) => p.id)).toEqual(["a"]);
    expect(visiblePoints(ps, false)).toHaveLength(2);
  });
  test("the Record hand-off caps at 60 and links to the Sequence tab", () => {
    const ids = Array.from({ length: 75 }, (_, i) => "s" + i);
    const h = recordHandOff(ids);
    expect(h.songIds).toHaveLength(60);
    expect(h.note).toContain("15 left out");
    expect(recordHandOff(["a"]).note).toBe("");
    expect(sequenceHref("rec-1")).toBe("/records/rec-1?sequence=arc");
  });
  test("the Board link carries the shared filters and the selection's Muse", () => {
    expect(boardHref(f)).toBe("/board?session=s1&state=draft&sound=era%3A90s&sound=mood%3Achill&q=rain");
    expect(boardHref(f, "m1")).toContain("muse=m1");
    expect(boardHref({ ...f, session: "", states: [], sounds: [], q: "" })).toBe("/board");
  });
  test("a shared Muse needs every selected point to carry the same Muse index", () => {
    const ps = [pt("a", { mi: 1 }), pt("b", { mi: 1 }), pt("c", { mi: 0 }), pt("d")];
    expect(sharedMuse(ps, ["m0", "m1"], new Set(["a", "b"]))).toBe("m1");
    expect(sharedMuse(ps, ["m0", "m1"], new Set(["a", "c"]))).toBe("");
    expect(sharedMuse(ps, ["m0", "m1"], new Set(["a", "d"]))).toBe("");
    expect(sharedMuse(ps, ["m0", "m1"], new Set())).toBe("");
  });
  test("selection gestures: toggle flips, add unions, clear empties", () => {
    const s = new Set(["a", "b"]);
    expect([...applySelect(s, ["b", "c"], "toggle")].sort()).toEqual(["a", "c"]);
    expect([...applySelect(s, ["b", "c"], "add")].sort()).toEqual(["a", "b", "c"]);
    expect(applySelect(s, [], "clear").size).toBe(0);
    expect(s.size).toBe(2);
  });
  test("readyOnce signals once", () => {
    let n = 0;
    const r = readyOnce(() => n++);
    r();
    r();
    r();
    expect(n).toBe(1);
  });
});
