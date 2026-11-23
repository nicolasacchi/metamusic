import { describe, expect, test } from "bun:test";
import { HeadsetGrammar, STRONG_WINDOW_MS, mmss, type HeadsetEvent, type HeadsetPos, type HeadsetPreset } from "./headset";

function rig(preset: HeadsetPreset = "prev_marks") {
  let t = 0;
  const timers: { at: number; fn: () => void; id: number }[] = [];
  let nid = 0;
  const events: HeadsetEvent[] = [];
  const flashes: string[] = [];
  const seeks: HeadsetPos[] = [];
  let vib = 0;
  const state = { pos: { side: 0, sideMs: 72_000, chapter: 3 } as HeadsetPos | null };
  const g = new HeadsetGrammar(
    {
      position: () => state.pos,
      emit: (e) => events.push(e),
      seekNext: (p) => seeks.push(p),
      setTimer: (fn, ms) => {
        const id = ++nid;
        timers.push({ at: t + ms, fn, id });
        return id;
      },
      clearTimer: (h) => {
        const i = timers.findIndex((x) => x.id === h);
        if (i >= 0) timers.splice(i, 1);
      },
      flash: (s) => flashes.push(s),
      vibrate: () => void vib++,
      latencyMs: 400,
    },
    preset,
  );
  const advance = (ms: number) => {
    t += ms;
    for (const x of [...timers].sort((a, b) => a.at - b.at)) {
      if (x.at <= t) {
        timers.splice(timers.indexOf(x), 1);
        x.fn();
      }
    }
  };
  return { g, events, flashes, seeks, state, advance, vib: () => vib, timers };
}

describe("headset grammar", () => {
  test("one previoustrack press is a marker, committed after the 3 s window", () => {
    const r = rig();
    r.g.press("previoustrack");
    expect(r.events).toHaveLength(0);
    expect(r.flashes).toEqual(["Marker 3 @ 1:12"]);
    r.advance(STRONG_WINDOW_MS - 1);
    expect(r.events).toHaveLength(0);
    r.advance(1);
    expect(r.events).toEqual([{ kind: "marker", side: 0, chapter: 3, side_ms: 72_000, latency_ms: 400 }]);
  });

  test("two presses inside the window are ONE strong marker at the first press", () => {
    const r = rig();
    r.g.press("previoustrack");
    r.state.pos = { side: 0, sideMs: 74_000, chapter: 3 };
    r.advance(2000);
    r.g.press("previoustrack");
    expect(r.events).toEqual([{ kind: "strong_marker", side: 0, chapter: 3, side_ms: 72_000, latency_ms: 400 }]);
    expect(r.flashes.at(-1)).toBe("Keep 3 @ 1:12");
    r.advance(10_000);
    expect(r.events).toHaveLength(1); // the buffered marker was dropped, not committed later
    expect(r.timers).toHaveLength(0);
  });

  test("a third press after a strong marker starts a new marker", () => {
    const r = rig();
    r.g.press("previoustrack");
    r.g.press("previoustrack");
    r.g.press("previoustrack");
    r.advance(STRONG_WINDOW_MS);
    expect(r.events.map((e) => e.kind)).toEqual(["strong_marker", "marker"]);
  });

  test("nexttrack is a skip at the position and a seek to the next chapter", () => {
    const r = rig();
    r.g.press("nexttrack");
    expect(r.events).toEqual([{ kind: "skip", side: 0, chapter: 3, side_ms: 72_000 }]);
    expect(r.seeks).toEqual([{ side: 0, sideMs: 72_000, chapter: 3 }]);
    expect(r.vib()).toBe(1);
  });

  test("a skip commits a buffered marker first, in that order", () => {
    const r = rig();
    r.g.press("previoustrack");
    r.g.press("nexttrack");
    expect(r.events.map((e) => e.kind)).toEqual(["marker", "skip"]);
    r.advance(10_000);
    expect(r.events).toHaveLength(2);
  });

  test("next_marks swaps the two buttons", () => {
    const r = rig("next_marks");
    r.g.press("nexttrack");
    r.g.press("nexttrack");
    r.g.press("previoustrack");
    expect(r.events.map((e) => e.kind)).toEqual(["strong_marker", "skip"]);
  });

  test("no position, no event", () => {
    const r = rig();
    r.state.pos = null;
    r.g.press("previoustrack");
    r.g.press("nexttrack");
    expect(r.events).toHaveLength(0);
    expect(r.timers).toHaveLength(0);
  });

  test("flush and dispose commit a buffered marker", () => {
    const r = rig();
    r.g.press("previoustrack");
    r.g.dispose();
    expect(r.events.map((e) => e.kind)).toEqual(["marker"]);
    r.g.flush();
    expect(r.events).toHaveLength(1);
  });

  test("latency is clamped to 0..5000", () => {
    const r = rig();
    const g = new HeadsetGrammar({ position: () => r.state.pos, emit: (e) => r.events.push(e), seekNext: () => {}, setTimer: () => 1, clearTimer: () => {}, latencyMs: 99_999 });
    g.mark();
    g.mark();
    expect(r.events[0]?.latency_ms).toBe(5000);
  });
});

describe("chapter progress", () => {
  const p = (chapter: number, sideMs = 0, side = 0): HeadsetPos => ({ side, sideMs, chapter });

  test("playing on completes the chapter left and emits play for the one entered, once", () => {
    const r = rig();
    r.g.observe(p(1, 1000));
    r.g.observe(p(2, 200_000), p(1, 199_000));
    r.g.observe(p(2, 201_000));
    expect(r.events.map((e) => `${e.kind}${e.chapter}`)).toEqual(["play1", "complete1", "play2"]);
    expect(r.events[1]?.side_ms).toBe(199_000);
  });

  test("a skipped chapter is not completed", () => {
    const r = rig();
    r.state.pos = p(1, 5000);
    r.g.observe(p(1, 5000));
    r.g.press("nexttrack");
    r.g.jumped();
    r.g.observe(p(2, 100_000));
    expect(r.events.map((e) => `${e.kind}${e.chapter}`)).toEqual(["play1", "skip1", "play2"]);
  });

  test("a jump forgets where it was: no complete for what a seek left", () => {
    const r = rig();
    r.g.observe(p(1, 1000));
    r.g.jumped();
    r.g.observe(p(2, 1000));
    expect(r.events.map((e) => `${e.kind}${e.chapter}`)).toEqual(["play1", "play2"]);
  });

  test("a jump over several chapters never completes anything", () => {
    const r = rig();
    r.g.observe(p(1, 1000));
    r.g.observe(p(4, 900_000));
    expect(r.events.map((e) => `${e.kind}${e.chapter}`)).toEqual(["play1", "play4"]);
  });

  test("a side that plays to its end completes its last chapter, and the next side's first chapter does not complete it twice", () => {
    const r = rig();
    r.g.observe(p(5, 1000, 0));
    r.g.ended(p(5, 1_200_000, 0));
    r.g.observe(p(6, 0, 1), p(5, 1_200_000, 0));
    expect(r.events.map((e) => `${e.kind}${e.chapter}`)).toEqual(["play5", "complete5", "play6"]);
  });

  test("going back to a chapter does not play it again", () => {
    const r = rig();
    r.g.observe(p(1));
    r.g.jumped();
    r.g.observe(p(2));
    r.g.jumped();
    r.g.observe(p(1));
    expect(r.events.filter((e) => e.kind === "play")).toHaveLength(2);
  });
});

test("mmss", () => {
  expect(mmss(72_000)).toBe("1:12");
  expect(mmss(-5)).toBe("0:00");
});
