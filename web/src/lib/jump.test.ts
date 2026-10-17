import { describe, expect, test } from "bun:test";
import { jumpTo, type JumpPlayer } from "./jump";

function fake(trackId: string, paused: boolean, duration = 100) {
  const calls: string[] = [];
  const player: JumpPlayer = {
    state: { trackId, duration, current: 12 },
    title: "Old title",
    playNext: (id, quiet) => void calls.push(`playNext ${id} ${String(quiet)}`),
    next: (at) => void calls.push(`next ${String(at)}`),
    el: { paused },
    seek: (s) => void calls.push(`seek ${s}`),
    resume: () => void calls.push("resume"),
    play: (id, q, o) => void calls.push(`play ${id} ${String(q)} ${JSON.stringify(o)}`),
  };
  const nav: string[] = [];
  const backs: string[] = [];
  const backRuns: (() => void)[] = [];
  return {
    player,
    calls,
    nav,
    backs,
    backRuns,
    deps: {
      player,
      navigate: (to: string) => void nav.push(to),
      offerBack: (title: string, run: () => void) => {
        backs.push(title);
        backRuns.push(run);
      },
    },
  };
}

describe("jumpTo", () => {
  test("same track, paused: seek then resume", () => {
    const f = fake("t1", true);
    jumpTo("t1", 72, f.deps);
    expect(f.calls).toEqual(["seek 72", "resume"]);
    expect(f.nav).toEqual([]);
  });
  test("same track, playing: seek only", () => {
    const f = fake("t1", false);
    jumpTo("t1", 72, f.deps);
    expect(f.calls).toEqual(["seek 72"]);
  });
  test("other track while one is loaded: play next + next + seek, queue kept, Back offered", () => {
    const f = fake("t1", false);
    jumpTo("t2", 72, f.deps);
    expect(f.nav).toEqual(["/tracks/t2?t=72"]);
    expect(f.calls).toEqual(["playNext t2 true", "next 72"]);
    expect(f.backs).toEqual(["Old title"]);
    f.backRuns[0]!();
    expect(f.calls.at(-1)).toBe('play t1 undefined {"startAt":12,"title":"Old title"}');
  });
  test("nothing loaded: plays the take with startAt (no Back offer)", () => {
    const f = fake("", true);
    jumpTo("t2", 72, f.deps);
    expect(f.nav).toEqual(["/tracks/t2?t=72"]);
    expect(f.calls).toEqual(['play t2 undefined {"startAt":72}']);
    expect(f.backs).toEqual([]);
  });
  test("t is clamped to [0, duration]", () => {
    const f = fake("t1", false, 100);
    jumpTo("t1", 500, f.deps);
    jumpTo("t1", -4, f.deps);
    expect(f.calls).toEqual(["seek 100", "seek 0"]);
  });
  test("non-finite t becomes 0 and ids are url-encoded", () => {
    const f = fake("t1", false);
    jumpTo("a b", NaN, f.deps);
    expect(f.nav).toEqual(["/tracks/a%20b?t=0"]);
  });
});
