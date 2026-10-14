import { describe, expect, test } from "bun:test";
import { installMediaSession, type MSEnv } from "./mediaSession";

function setup(opts: { duration?: number; currentTime?: number } = {}) {
  const handlers = new Map<string, unknown>();
  const sets: { action: string; h: unknown }[] = [];
  const positions: { duration: number; position: number; playbackRate: number }[] = [];
  let t = 0;
  const doc = new EventTarget() as unknown as MSEnv["doc"] & EventTarget;
  Object.defineProperty(doc, "visibilityState", { value: "visible" });
  const win = new EventTarget() as unknown as MSEnv["win"] & EventTarget;
  const ms = {
    metadata: null as unknown,
    playbackState: "none" as string,
    setActionHandler(a: string, h: unknown) {
      handlers.set(a, h);
      sets.push({ action: a, h });
    },
    setPositionState(s: never) {
      positions.push(s);
    },
  };
  const el = Object.assign(new EventTarget(), {
    duration: opts.duration ?? 100,
    currentTime: opts.currentTime ?? 10,
    playbackRate: 1,
    played: 0,
    paused: false,
    play() {
      this.played++;
      return Promise.resolve();
    },
    pause() {
      this.paused = true;
    },
  });
  const calls: string[] = [];
  const player = {
    title: "T",
    album: "Alb",
    hasNext: true,
    playIntent: undefined as boolean | undefined,
    state: { trackId: "a", endOfQueue: false, pendingPlay: false },
    prev: () => void calls.push("prev"),
    next: () => void calls.push("next"),
    seek: (s: number) => void calls.push("seek:" + s),
    resume: () => void calls.push("resume"),
    pause: () => void calls.push("pause"),
  };
  class MM {
    constructor(public init: unknown) {}
  }
  const cleanup = installMediaSession(player, el as unknown as HTMLAudioElement, {
    mediaSession: ms,
    MediaMetadata: MM,
    doc,
    win,
    now: () => t,
  });
  return { player, ms, el, handlers, sets, positions, calls, doc, win, cleanup, tick: (n: number) => (t += n) };
}

describe("mediaSession", () => {
  test("registers 5 handlers and nulls seekforward/backward", () => {
    const { handlers } = setup();
    for (const a of ["play", "pause", "previoustrack", "nexttrack", "seekto"]) expect(typeof handlers.get(a)).toBe("function");
    expect(handlers.get("seekforward")).toBeNull();
    expect(handlers.get("seekbackward")).toBeNull();
  });

  test("handlers drive the player", () => {
    const { handlers, calls, el } = setup();
    (handlers.get("previoustrack") as () => void)();
    (handlers.get("nexttrack") as () => void)();
    (handlers.get("seekto") as (d: unknown) => void)({ seekTime: 12 });
    (handlers.get("pause") as () => void)();
    (handlers.get("play") as () => void)();
    expect(calls).toEqual(["prev", "next", "seek:12", "pause", "resume"]);
    expect(el.played).toBe(0);
  });

  test("re-registers on playing, visibility and pageshow", () => {
    const { sets, el, doc, win } = setup();
    const n = () => sets.length;
    const base = n();
    el.dispatchEvent(new Event("playing"));
    expect(n()).toBeGreaterThan(base);
    const b2 = n();
    (doc as unknown as EventTarget).dispatchEvent(new Event("visibilitychange"));
    expect(n()).toBeGreaterThan(b2);
    const b3 = n();
    (win as unknown as EventTarget).dispatchEvent(new Event("pageshow"));
    expect(n()).toBeGreaterThan(b3);
  });

  test("metadata uses title, album and the 512 icon", () => {
    const { ms, el } = setup();
    el.dispatchEvent(new Event("playing"));
    const init = (ms.metadata as { init: { title: string; artist: string; album: string; artwork: { src: string }[] } }).init;
    expect(init.title).toBe("T");
    expect(init.artist).toBe("metamusic");
    expect(init.album).toBe("Alb");
    expect(init.artwork[0]!.src).toBe("/icons/icon-512.png");
  });

  test("a track change re-registers handlers and a title change refreshes metadata", () => {
    const { ms, el, sets } = setup();
    el.dispatchEvent(new Event("playing"));
    const n = sets.length;
    el.dispatchEvent(new Event("loadedmetadata"));
    expect(sets.length).toBeGreaterThan(n);
    const before = ms.metadata;
    el.dispatchEvent(new Event("mm:meta"));
    expect(ms.metadata).not.toBe(before);
  });

  test("never calls setPositionState with NaN, Infinity or 0 duration", () => {
    for (const d of [NaN, Infinity, 0]) {
      const { el, positions } = setup({ duration: d });
      el.dispatchEvent(new Event("loadedmetadata"));
      el.dispatchEvent(new Event("seeked"));
      el.dispatchEvent(new Event("timeupdate"));
      expect(positions).toHaveLength(0);
    }
  });

  test("clamps position into [0, duration]", () => {
    const hi = setup({ duration: 50, currentTime: 80 });
    hi.el.dispatchEvent(new Event("seeked"));
    expect(hi.positions[0]!.position).toBe(50);
    const lo = setup({ duration: 50, currentTime: -3 });
    lo.el.dispatchEvent(new Event("seeked"));
    expect(lo.positions[0]!.position).toBe(0);
    expect(lo.positions[0]!.playbackRate).toBeGreaterThan(0);
  });

  test("timeupdate position is throttled to 1/s", () => {
    const s = setup();
    s.tick(5000);
    s.el.dispatchEvent(new Event("timeupdate"));
    s.el.dispatchEvent(new Event("timeupdate"));
    expect(s.positions).toHaveLength(1);
    s.tick(1000);
    s.el.dispatchEvent(new Event("timeupdate"));
    expect(s.positions).toHaveLength(2);
  });

  test("no-op without mediaSession", () => {
    const el = new EventTarget() as unknown as HTMLAudioElement;
    const off = installMediaSession(
      { title: "", album: "", state: { trackId: "" }, prev() {}, next() {}, seek() {}, resume() {}, pause() {} },
      el,
      { doc: new EventTarget() as never, win: new EventTarget() as never },
    );
    off();
  });

  test("playbackState follows the transport: playing, paused, none at the end of the queue", () => {
    const { ms, el, player } = setup();
    el.dispatchEvent(new Event("playing"));
    expect(ms.playbackState).toBe("playing");
    el.paused = true;
    el.dispatchEvent(new Event("pause"));
    expect(ms.playbackState).toBe("paused");
    el.paused = false;
    el.dispatchEvent(new Event("play"));
    expect(ms.playbackState).toBe("playing");
    // a blocked autoplay is a paused session, not a playing one
    player.state.pendingPlay = true;
    el.dispatchEvent(new Event("mm:state"));
    expect(ms.playbackState).toBe("paused");
    player.state.pendingPlay = false;
    player.state.endOfQueue = true;
    el.dispatchEvent(new Event("mm:state"));
    expect(ms.playbackState).toBe("none");
  });

  test("the transition to the next track keeps the session playing while the next source loads", () => {
    const { ms, el, player } = setup();
    el.dispatchEvent(new Event("playing"));
    player.playIntent = true;
    el.paused = true; // src swapped: the element is paused until play() takes effect
    el.dispatchEvent(new Event("mm:state"));
    expect(ms.playbackState).toBe("playing");
  });

  test("nexttrack is withdrawn when nothing is next and restored when something is", () => {
    const { handlers, el, player } = setup();
    player.hasNext = false;
    el.dispatchEvent(new Event("mm:state"));
    expect(handlers.get("nexttrack")).toBeNull();
    player.hasNext = true;
    el.dispatchEvent(new Event("mm:state"));
    expect(typeof handlers.get("nexttrack")).toBe("function");
  });

  test("a track change clears the stale position and refreshes the metadata", () => {
    const { ms, el, player, positions } = setup();
    el.dispatchEvent(new Event("loadedmetadata"));
    const n = positions.length;
    const before = ms.metadata;
    player.state.trackId = "b";
    el.dispatchEvent(new Event("mm:state"));
    expect(positions.length).toBe(n + 1);
    expect(positions.at(-1)).toBeUndefined();
    expect(ms.metadata).not.toBe(before);
    // an unrelated state change does not clear it again
    el.dispatchEvent(new Event("mm:state"));
    expect(positions.length).toBe(n + 1);
  });
});
