import { describe, expect, test } from "bun:test";
import { jumpTo, type JumpPlayer } from "./jump";

function fake(trackId: string, paused: boolean, duration = 100) {
  const calls: string[] = [];
  const player: JumpPlayer = {
    state: { trackId, duration },
    el: { paused },
    seek: (s) => void calls.push(`seek ${s}`),
    resume: () => void calls.push("resume"),
    play: (id, q, o) => void calls.push(`play ${id} ${String(q)} ${JSON.stringify(o)}`),
  };
  const nav: string[] = [];
  return { player, calls, nav, deps: { player, navigate: (to: string) => void nav.push(to) } };
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
  test("other track: navigates and plays with startAt", () => {
    const f = fake("t1", false);
    jumpTo("t2", 72, f.deps);
    expect(f.nav).toEqual(["/tracks/t2?t=72"]);
    expect(f.calls).toEqual(['play t2 undefined {"startAt":72}']);
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
