import { describe, expect, test } from "bun:test";
import {
  CrateSession,
  chapterAt,
  chapterTitle,
  duelPair,
  isMorningItem,
  isToday,
  morningItem,
  offlineSides,
  parseMorningItem,
  rateEvent,
  sideLabel,
  sideUrl,
  unratedCount,
  type MorningChapter,
  type MorningCrate,
  type MorningOutEvent,
} from "./morningCrate";

function ch(n: number, side: number, start: number, end: number, over: Partial<MorningChapter> = {}): MorningChapter {
  return {
    anchor: null,
    audio_start_ms: start + 4000,
    end_ms: end,
    gain_db: null,
    limited: null,
    lufs_after: null,
    markers: 0,
    n,
    side,
    skipped_at_ms: null,
    stars: null,
    start_ms: start,
    strong_markers: 0,
    track: null,
    verdict: "unheard",
    ...over,
  };
}

function crate(over: Partial<MorningCrate> = {}): MorningCrate {
  return {
    adjacency_relaxed: false,
    anchors: 1,
    built_at: "2026-11-23T07:01:00Z",
    chapters: [ch(1, 0, 0, 200_000), ch(2, 0, 200_000, 400_000), ch(3, 1, 0, 150_000)],
    created_at: "2026-11-23T07:00:00Z",
    duration_ms: 750_000,
    error_code: null,
    id: "c1",
    playlist_id: null,
    revealed: false,
    revealed_at: null,
    sides: [
      { bytes: 1000, chapters: 2, duration_ms: 400_000, expires_at: "2099-01-01T00:00:00Z", label: "Side A", removed: false, sha256: "a".repeat(64), side: 0, url: "/media/a.flac?x=1" },
      { bytes: 500, chapters: 1, duration_ms: 150_000, expires_at: "2099-01-01T00:00:00Z", label: "Side B", removed: false, sha256: "b".repeat(64), side: 1, url: "/media/b.flac?x=1" },
    ],
    state: "ready",
    takes: 3,
    trigger: "scheduled",
    ...over,
  };
}

describe("items", () => {
  test("round trip, and the player's non-take checks", () => {
    expect(morningItem("c1", 1)).toBe("morning:c1:1");
    expect(parseMorningItem("morning:c1:1")).toEqual({ crate: "c1", side: 1 });
    expect(parseMorningItem("morning:c1")).toBeNull();
    expect(parseMorningItem("tape:x")).toBeNull();
    expect(isMorningItem("morning:c1:0")).toBe(true);
    expect(isMorningItem("t1")).toBe(false);
  });
});

describe("views", () => {
  const v = crate();
  test("chapterAt picks the chapter whose slate has started", () => {
    expect(chapterAt(v, 0, 0)?.n).toBe(1);
    expect(chapterAt(v, 0, 199_999)?.n).toBe(1);
    expect(chapterAt(v, 0, 200_000)?.n).toBe(2);
    expect(chapterAt(v, 1, 5)?.n).toBe(3);
    expect(chapterAt(v, 5, 5)).toBeNull();
  });
  test("titles are blind until the reveal", () => {
    expect(chapterTitle(v, v.chapters[1]!)).toBe("Take 2 of 3");
    const r = crate({ revealed: true, chapters: [ch(1, 0, 0, 1, { track: { id: "t1", root_track_id: "r", title: "Alice Demo" } })] });
    expect(chapterTitle(r, r.chapters[0]!)).toBe("Alice Demo");
    const gone = crate({ revealed: true });
    expect(chapterTitle(gone, gone.chapters[0]!)).toBe("Take 1 of 3");
  });
  test("labels, counts, links", () => {
    expect(sideLabel(v, 1)).toBe("Side B");
    expect(sideLabel(v, 2)).toBe("Side C");
    expect(unratedCount(v)).toBe(3);
    expect(unratedCount(crate({ chapters: [ch(1, 0, 0, 1, { stars: 4 })] }))).toBe(0);
    expect(sideUrl(v.sides[0], Date.parse("2026-11-23T08:00:00Z"))).toBe("/media/a.flac?x=1");
    expect(sideUrl({ ...v.sides[0]!, expires_at: "2026-11-23T07:30:00Z" }, Date.parse("2026-11-23T08:00:00Z"))).toBeNull();
    expect(sideUrl({ ...v.sides[0]!, removed: true, url: null }, 0)).toBeNull();
    expect(offlineSides(v, 0).map((s) => s.side)).toEqual([0, 1]);
    expect(offlineSides(crate({ sides: [{ ...v.sides[0]!, removed: true, url: null }] }), 0)).toEqual([]);
  });
  test("isToday follows the local date", () => {
    expect(isToday(new Date(2026, 10, 23, 7).toISOString(), new Date(2026, 10, 23, 22))).toBe(true);
    expect(isToday(new Date(2026, 10, 22, 7).toISOString(), new Date(2026, 10, 23, 22))).toBe(false);
  });
  test("duelPair needs exactly two different real takes", () => {
    const r = crate({
      revealed: true,
      chapters: [
        ch(1, 0, 0, 1, { track: { id: "t1", root_track_id: "r", title: "A" } }),
        ch(2, 0, 1, 2, { track: { id: "t2", root_track_id: "r", title: "B" } }),
        ch(3, 0, 2, 3),
      ],
    });
    expect(duelPair(r, [1, 2])).toEqual(["t1", "t2"]);
    expect(duelPair(r, [1])).toBeNull();
    expect(duelPair(r, [1, 3])).toBeNull();
    expect(duelPair(r, [1, 1])).toBeNull();
  });
  test("rateEvent validates stars and positions at the chapter start", () => {
    const e = rateEvent(v, 2, 4, "id1", new Date("2026-11-23T08:00:00Z"));
    expect(e).toEqual({ crate_id: "c1", id: "id1", side: 0, chapter: 2, kind: "rate", side_ms: 200_000, client_at: "2026-11-23T08:00:00.000Z", stars: 4 });
    expect(rateEvent(v, 2, null, "id2", new Date())?.stars).toBeNull();
    expect(rateEvent(v, 2, 6, "x", new Date())).toBeNull();
    expect(rateEvent(v, 2, 2.5, "x", new Date())).toBeNull();
    expect(rateEvent(v, 9, 3, "x", new Date())).toBeNull();
  });
});

function session(view = crate()) {
  const out: MorningOutEvent[] = [];
  const seeks: number[] = [];
  const sides: [number, number][] = [];
  const finished: number[] = [];
  const described: string[] = [];
  const timers = new Map<number, () => void>();
  let tid = 0;
  let n = 0;
  const here = { trackId: "morning:c1:0", sec: 250 };
  const s = new CrateSession(view, {
    position: () => here,
    submit: (e) => out.push(e),
    seek: (x) => seeks.push(x),
    playSide: (a, b) => sides.push([a, b]),
    finish: (x) => finished.push(x),
    setTimer: (fn) => {
      timers.set(++tid, fn);
      return tid;
    },
    clearTimer: (h) => void timers.delete(h as number),
    describe: (t, a) => described.push(`${t} / ${a}`),
    id: () => `e${++n}`,
    now: () => new Date("2026-11-23T08:00:00Z"),
    latencyMs: 0,
  });
  return { s, out, seeks, sides, finished, described, here, fire: () => [...timers.values()].forEach((f) => f()) };
}

describe("CrateSession", () => {
  test("the lock screen says Take k of N and Side A, never a title, before the reveal", () => {
    const r = session();
    r.s.tick();
    expect(r.described).toEqual(["Take 2 of 3 / Side A"]);
    r.s.tick();
    expect(r.described).toHaveLength(1);
  });

  test("nexttrack skips at the position and seeks to the next chapter's slate", () => {
    const r = session();
    r.s.tick();
    r.s.press("nexttrack");
    expect(r.out.map((e) => [e.kind, e.chapter, e.side_ms, e.crate_id, e.id])).toEqual([
      ["play", 2, 250_000, "c1", "e1"],
      ["skip", 2, 250_000, "c1", "e2"],
    ]);
    // chapter 3 is on side B: another side starts at its slate
    expect(r.sides).toEqual([[1, 0]]);
    expect(r.seeks).toEqual([]);
  });

  test("a skip inside a side seeks within it", () => {
    const r = session();
    r.here.sec = 50;
    r.s.press("nexttrack");
    expect(r.seeks).toEqual([200]);
    expect(r.out.at(-1)?.kind).toBe("skip");
  });

  test("a skip of the last chapter lets the crate end", () => {
    const r = session();
    r.here.trackId = "morning:c1:1";
    r.here.sec = 10;
    r.s.press("nexttrack");
    expect(r.finished).toEqual([1]);
  });

  test("two previoustrack presses are one keep at the first press; a lone one is a marker after 3 s", () => {
    const r = session();
    r.s.press("previoustrack");
    r.here.sec = 252;
    r.s.press("previoustrack");
    expect(r.out.map((e) => [e.kind, e.side_ms])).toEqual([["strong_marker", 250_000]]);
    r.s.press("previoustrack");
    r.fire();
    expect(r.out.at(-1)).toMatchObject({ kind: "marker", side_ms: 252_000, latency_ms: 0 });
  });

  test("a different crate or a plain track is not ours: no events", () => {
    const r = session();
    r.here.trackId = "morning:other:0";
    r.s.press("nexttrack");
    r.here.trackId = "t1";
    r.s.press("previoustrack");
    r.s.tick();
    expect(r.out).toHaveLength(0);
  });

  test("playing past a chapter's end completes it; the side's end completes the last one once", () => {
    const r = session();
    r.here.sec = 190;
    r.s.tick();
    r.here.sec = 201;
    r.s.tick();
    r.s.ended("morning:c1:0");
    r.s.ended("morning:c1:0");
    expect(r.out.map((e) => `${e.kind}${e.chapter}@${e.side_ms}`)).toEqual(["play1@190000", "complete1@200000", "play2@201000", "complete2@400000"]);
  });

  test("back goes to the start of the chapter, or the one before when it has just begun", () => {
    const r = session();
    r.here.sec = 260;
    r.s.back();
    expect(r.seeks).toEqual([200]);
    r.here.sec = 201;
    r.s.back();
    expect(r.seeks).toEqual([200, 0]);
    expect(r.out).toHaveLength(0);
  });

  test("a jump forgets the chapter left", () => {
    const r = session();
    r.here.sec = 190;
    r.s.tick();
    r.s.jumped();
    r.here.sec = 250;
    r.s.tick();
    expect(r.out.map((e) => e.kind + e.chapter)).toEqual(["play1", "play2"]);
  });

  test("update swaps the view: the reveal shows titles on the next tick", () => {
    const r = session();
    r.s.tick();
    r.s.update(crate({ revealed: true, chapters: [ch(1, 0, 0, 200_000), ch(2, 0, 200_000, 400_000, { track: { id: "t2", root_track_id: "r", title: "Bob Song" } }), ch(3, 1, 0, 1)] }));
    r.s.tick();
    expect(r.described.at(-1)).toBe("Bob Song / Side A");
  });
});

import { SETTING_RANGES, crateLine, formToPatch, minutesText, settingsToForm, type MorningSettings, type MorningCrateSummary } from "./morningCrate";

const settings = (): MorningSettings => ({
  anchors: 1, at: "07:00", enabled: true, include_truncated: false, keep_days: 7, max_takes: 12, minutes: 40, side_minutes: 15,
  updated_at: "2026-11-23T07:00:00Z", updated_by: { kind: "system", id: "system:migration", name: "migration" }, wait_minutes: 45,
});

describe("settings form", () => {
  test("round trip: an untouched form is 'Nothing changed'", () => {
    const f = settingsToForm(settings());
    expect(f.minutes).toBe("40");
    expect(formToPatch(f, settings())).toEqual({ ok: false, error: "Nothing changed." });
  });
  test("only changes go into the patch", () => {
    const f = settingsToForm(settings());
    f.at = "06:30";
    f.minutes = "35";
    f.enabled = false;
    expect(formToPatch(f, settings())).toEqual({ ok: true, body: { at: "06:30", minutes: 35, enabled: false } });
  });
  test("bad figures name the field and the range", () => {
    for (const [k, v] of [["minutes", "4"], ["minutes", "121"], ["side_minutes", "16"], ["max_takes", "0"], ["anchors", "3"], ["keep_days", "x"], ["wait_minutes", "1.5"], ["minutes", ""]] as const) {
      const f = settingsToForm(settings());
      f[k] = v;
      const r = formToPatch(f, settings());
      expect(r.ok).toBe(false);
      if (!r.ok) expect(r.error).toContain(SETTING_RANGES[k].label);
    }
    const f = settingsToForm(settings());
    f.at = "7:00";
    expect(formToPatch(f, settings())).toMatchObject({ ok: false });
    f.at = "24:00";
    expect(formToPatch(f, settings())).toMatchObject({ ok: false });
  });
});

describe("list lines", () => {
  const s = (o: Partial<MorningCrateSummary>): MorningCrateSummary => ({ anchors: 0, built_at: null, created_at: "2026-11-23T07:00:00Z", duration_ms: 2_400_000, error_code: null, id: "c", revealed: false, revealed_at: null, sides: 3, state: "ready", takes: 12, trigger: "scheduled", ...o });
  test("each state reads plainly", () => {
    expect(crateLine(s({}))).toBe("12 takes, 40 min, 3 sides, blind.");
    expect(crateLine(s({ revealed: true, sides: 1 }))).toBe("12 takes, 40 min, 1 side, revealed.");
    expect(crateLine(s({ state: "building" }))).toBe("Building…");
    expect(crateLine(s({ state: "empty" }))).toContain("Nothing");
    expect(crateLine(s({ state: "failed", error_code: "TOOL_FAILED" }))).toBe("Failed (TOOL_FAILED).");
    expect(minutesText(10)).toBe("1 min");
  });
});

import { loadPreset, savePreset } from "./morningCrate";
describe("headset preset storage", () => {
  test("defaults to previous-marks; remembers the swap; a broken storage never throws", () => {
    const m = new Map<string, string>();
    const st = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) };
    expect(loadPreset(st)).toBe("prev_marks");
    savePreset("next_marks", st);
    expect(loadPreset(st)).toBe("next_marks");
    const bad = { getItem: () => { throw new Error("x"); }, setItem: () => { throw new Error("x"); } };
    expect(loadPreset(bad)).toBe("prev_marks");
    expect(() => savePreset("next_marks", bad)).not.toThrow();
    expect(loadPreset(undefined)).toBe("prev_marks");
  });
  test("a session follows setPreset", () => {
    const out: string[] = [];
    const s = new CrateSession(crate(), {
      position: () => ({ trackId: "morning:c1:0", sec: 10 }), submit: (e) => void out.push(e.kind), seek: () => {}, playSide: () => {}, finish: () => {},
      setTimer: () => 1, clearTimer: () => {}, id: () => "i", now: () => new Date(), preset: "prev_marks",
    });
    s.setPreset("next_marks");
    s.press("previoustrack");
    expect(out).toEqual(["skip"]);
  });
});
