import { describe, expect, test } from "bun:test";
import { chapterAt, chapterNames, chapterStartMs, enqueueRecord, playAsTape, playRecord, seekToChapter, type ExportResult, type TapeEnv } from "./recordPlayer";
import { gainMap, isTapeItem, recordIdOfHref, takeQueue, tapeToLinks, type RecordView, type Tape } from "./recordMedia";

type Slot = { id: string; take: { id: string; title: string; status?: string } | null; state?: string; gain_db?: number | null; song?: string };
function view(slots: Slot[], over: Record<string, unknown> = {}): RecordView {
  return {
    record: { id: "r1", title: "Night Drive", revision: 4, ...over },
    slots: slots.map((s, i) => ({
      id: s.id,
      position: i + 1,
      state: s.state ?? (s.take ? "ok" : "empty"),
      gain_db: s.gain_db ?? null,
      take: s.take ? { status: "approved", ...s.take } : null,
      song: { id: "s" + i, title: s.song ?? "Song " + i },
    })),
  } as unknown as RecordView;
}
const V = view([
  { id: "sl1", take: { id: "t1", title: "One" }, gain_db: -3.5 },
  { id: "sl2", take: null },
  { id: "sl3", take: { id: "t3", title: "Three" }, gain_db: null },
  { id: "sl4", take: { id: "t1", title: "One" }, gain_db: -3.5 },
  { id: "sl5", take: { id: "t5", title: "Gone", status: "absent" }, state: "pin_ineligible", gain_db: 2 },
  { id: "sl6", take: { id: "t6", title: "Six" }, gain_db: 1.25 },
]);

describe("record data", () => {
  test("queue skips empty slots, absent Takes and repeats", () => {
    expect(takeQueue(V)).toEqual(["t1", "t3", "t6"]);
  });
  test("gains map take_id to gain_db; unmeasured Takes are absent", () => {
    expect(gainMap(V)).toEqual({ t1: -3.5, t6: 1.25 });
  });
  test("source href", () => {
    expect(recordIdOfHref("/records/abc_1-2")).toBe("abc_1-2");
    for (const h of ["/records/", "/records/a/b", "/records/a?x=1", "/crates/a", "", null, "//records/a"]) expect(recordIdOfHref(h)).toBeNull();
  });
  test("tape links: ready only, always a flac link", () => {
    const t = tape({ state: "ready" });
    const l = tapeToLinks("tape:t1", t, Date.parse("2026-01-01T00:00:00Z"))!;
    expect(l.track_id).toBe("tape:t1");
    expect(l.flac!.url).toBe("/media/tape.flac?x");
    expect(l.ttl_s).toBe(3600);
    expect(tapeToLinks("tape:t1", tape({ state: "running", download: null }), 0)).toBeNull();
  });
});

function tape(o: Partial<Tape> = {}): Tape {
  return {
    id: "t1",
    record_id: "r1",
    record_revision: 4,
    state: "ready",
    contains_lossy: false,
    sha256: "abc",
    download: { url: "/media/tape.flac?x", bytes: 9, filename: "n.flac", expires_at: "2026-01-01T01:00:00Z" },
    chapters: [
      { index: 1, slot_id: "sl1", start_ms: 0, samples: 1 },
      { index: 2, slot_id: "sl3", start_ms: 61_500, samples: 1 },
      { index: 3, slot_id: "sl6", start_ms: 120_000, samples: 1 },
    ],
    error: null,
    ...o,
  } as unknown as Tape;
}

function fakePlayer() {
  const calls: { play: unknown[][]; load: unknown[][]; enq: string[]; merged: Record<string, number>[]; seeks: number[] } = { play: [], load: [], enq: [], merged: [], seeks: [] };
  const state = { trackId: "" };
  return {
    calls,
    state,
    play: (...a: unknown[]) => void calls.play.push(a),
    load: (...a: unknown[]) => void calls.load.push(a),
    enqueue: (id: string) => void calls.enq.push(id),
    mergeGains: (g: Record<string, number>) => void calls.merged.push(g),
    seek: (s: number) => void calls.seeks.push(s),
  };
}

describe("playRecord", () => {
  test("plays the first resolved Take with the Record's queue, label, href and gains", () => {
    const p = fakePlayer();
    expect(playRecord(V, {}, p as never)).toBe(true);
    expect(p.calls.play).toEqual([
      [
        "t1",
        ["t1", "t3", "t6"],
        { title: "One", subtitle: "Night Drive", source: { label: "Night Drive", href: "/records/r1" }, undo: true, gains: { t1: -3.5, t6: 1.25 } },
      ],
    ]);
  });
  test("from: starts at that Take; an unknown or empty one falls back to the first", () => {
    const p = fakePlayer();
    playRecord(V, { from: "t3" }, p as never);
    playRecord(V, { from: "nope" }, p as never);
    expect(p.calls.play.map((c) => c[0])).toEqual(["t3", "t1"]);
  });
  test("nothing playable: no call", () => {
    const p = fakePlayer();
    expect(playRecord(view([{ id: "a", take: null }]), {}, p as never)).toBe(false);
    expect(p.calls.play).toEqual([]);
  });
  test("enqueue: appends with gains, or loads paused when nothing is loaded", () => {
    const p = fakePlayer();
    expect(enqueueRecord(V, p as never)).toBe(true);
    expect(p.calls.load[0]![0]).toBe("t1");
    expect(p.calls.load[0]![2]).toMatchObject({ gains: { t1: -3.5, t6: 1.25 }, source: { href: "/records/r1" } });
    p.state.trackId = "x";
    enqueueRecord(V, p as never);
    expect(p.calls.enq).toEqual(["t1", "t3", "t6"]);
    expect(p.calls.merged).toEqual([{ t1: -3.5, t6: 1.25 }]);
  });
});

function env(o: { tapes?: unknown[]; byId?: Record<string, Tape>; exp?: ExportResult | (() => ExportResult); seq?: Tape[] } = {}) {
  const p = fakePlayer();
  const log: string[] = [];
  let t = 0;
  const seq = [...(o.seq ?? [])];
  const e: TapeEnv = {
    player: p as never,
    tape: async (id) => {
      log.push("get:" + id);
      return seq.length ? seq.shift()! : (o.byId?.[id] ?? null);
    },
    tapes: async () => ({ title: "Night Drive", tapes: (o.tapes ?? []) as never }),
    exportTape: async (_r, rev, key) => {
      log.push(`export:${rev}:${key.length > 8 ? "key" : "?"}`);
      return typeof o.exp === "function" ? o.exp() : (o.exp ?? { status: 501, code: "NOT_IMPLEMENTED", message: "x" });
    },
    sleep: async (ms) => void (t += ms),
    now: () => t,
  };
  return { e, p, log };
}
const brief = (id: string, state = "ready", at = "2026-01-01T00:00:00Z") => ({ id, state, created_at: at });

describe("playAsTape", () => {
  test("a ready tape of the current revision plays without an export", async () => {
    const { e, p, log } = env({ tapes: [brief("t1")], byId: { t1: tape() } });
    const r = await playAsTape("r1", 4, {}, e);
    expect(r.kind).toBe("playing");
    expect(log).toEqual(["get:t1"]);
    expect(p.calls.play).toEqual([["tape:t1", ["tape:t1"], { title: "Night Drive", subtitle: "Tape", source: { label: "Night Drive", href: "/records/r1" }, undo: true }]]);
    expect(isTapeItem(p.calls.play[0]![0] as string)).toBe(true);
  });

  test("a tape of an older revision is not reused: export, then play", async () => {
    const { e, p, log } = env({ tapes: [brief("t0")], byId: { t0: tape({ id: "t0", record_revision: 3 }) }, exp: { tape: tape({ id: "t2" }) } });
    const r = await playAsTape("r1", 4, {}, e);
    expect(log).toEqual(["get:t0", "export:4:key"]);
    expect(r.kind).toBe("playing");
    expect(p.calls.play[0]![0]).toBe("tape:t2");
  });

  test("newest ready tapes first, at most three are opened", async () => {
    const tapes = ["a", "b", "c", "d"].map((id, i) => brief(id, "ready", `2026-01-0${i + 1}T00:00:00Z`));
    const { e, log } = env({ tapes, exp: { status: 409, code: "STALE_REVISION", message: "m" } });
    await playAsTape("r1", 4, {}, e);
    expect(log).toEqual(["get:d", "get:c", "get:b", "export:4:key"]);
  });

  test("queued then running then ready: polls, reports progress, then plays", async () => {
    const run = tape({ state: "running", download: null });
    const { e, p } = env({ exp: { tape: tape({ state: "queued", download: null }) }, seq: [run, tape()] });
    const seen: string[] = [];
    const r = await playAsTape("r1", 4, { tapes: [], title: "Night Drive", onProgress: (t) => void seen.push(t.state) }, e);
    expect(seen).toEqual(["queued", "running"]);
    expect(r.kind).toBe("playing");
    expect(p.calls.play).toHaveLength(1);
  });

  test("wait:false returns pending without polling", async () => {
    const { e, p, log } = env({ exp: { tape: tape({ state: "queued", download: null }) } });
    const r = await playAsTape("r1", 4, { tapes: [], title: "T", wait: false }, e);
    expect(r.kind).toBe("pending");
    expect(p.calls.play).toEqual([]);
    expect(log).toEqual(["export:4:key"]);
  });

  test("stops polling after 15 minutes", async () => {
    const q = tape({ state: "running", download: null });
    const { e } = env({ exp: { tape: q }, byId: { t1: q } });
    const r = await playAsTape("r1", 4, { tapes: [], title: "T" }, e);
    expect(r.kind).toBe("pending");
  });

  test("an abort stops the wait", async () => {
    const ac = new AbortController();
    const q = tape({ state: "running", download: null });
    const { e } = env({ exp: { tape: q }, byId: { t1: q } });
    const r = await playAsTape("r1", 4, { tapes: [], title: "T", signal: ac.signal, onProgress: () => ac.abort() }, e);
    expect(r.kind).toBe("pending");
  });

  test("a failed export row reports its code", async () => {
    const { e, p } = env({ exp: { tape: tape({ state: "failed", download: null, error: { code: "TAPE_VERIFY_FAILED", message: "verify" } }) } });
    const r = await playAsTape("r1", 4, { tapes: [], title: "T" }, e);
    expect(r).toMatchObject({ kind: "failed", code: "TAPE_VERIFY_FAILED", message: "verify" });
    expect(p.calls.play).toEqual([]);
  });

  test("501 (tape service not built yet) is a graceful failure", async () => {
    const { e, p } = env({ exp: { status: 501, code: "NOT_IMPLEMENTED", message: "x" } });
    const r = await playAsTape("r1", 4, { tapes: [], title: "T" }, e);
    expect(r).toEqual({ kind: "failed", code: "NOT_IMPLEMENTED", message: "Tape export is not available yet" });
    expect(p.calls.play).toEqual([]);
  });

  test("409s keep the server's code and text", async () => {
    const { e } = env({ exp: { status: 409, code: "TAPE_NEEDS_REVIEW", message: "2 slots need review" } });
    expect(await playAsTape("r1", 4, { tapes: [], title: "T" }, e)).toEqual({ kind: "failed", code: "TAPE_NEEDS_REVIEW", message: "2 slots need review" });
  });

  test("get_tape unavailable (501 reads as null) never throws", async () => {
    const { e } = env({ tapes: [brief("t1")], exp: { status: 501, code: "NOT_IMPLEMENTED", message: "x" } });
    expect((await playAsTape("r1", 4, {}, e)).kind).toBe("failed");
  });
});

describe("chapters", () => {
  const ch = tape().chapters!;
  test("index to start_ms", () => {
    expect(chapterStartMs(ch, 2)).toBe(61_500);
    expect(chapterStartMs(ch, 9)).toBeNull();
    expect(chapterStartMs(null, 1)).toBeNull();
  });
  test("chapter at a position", () => {
    expect(chapterAt(ch, 0)).toBe(1);
    expect(chapterAt(ch, 61_499)).toBe(1);
    expect(chapterAt(ch, 61_500)).toBe(2);
    expect(chapterAt(ch, 999_999)).toBe(3);
    expect(chapterAt([], 5)).toBe(-1);
  });
  test("seek only while that tape is the current item", () => {
    const p = fakePlayer();
    const t = tape();
    expect(seekToChapter(t, 2, p as never)).toBe(false);
    p.state.trackId = "tape:t1";
    expect(seekToChapter(t, 2, p as never)).toBe(true);
    expect(seekToChapter(t, 9, p as never)).toBe(false);
    expect(p.calls.seeks).toEqual([61.5]);
  });
  test("names come from the Record's slots", () => {
    expect(chapterNames(V)).toMatchObject({ sl1: "One", sl2: "Song 1", sl3: "Three" });
  });
});
