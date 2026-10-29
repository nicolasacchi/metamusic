import { describe, expect, test } from "bun:test";
import { Player, type DeviceHooks, type MediaLinks } from "./player.svelte";
import { serialize } from "./playerStore";
import { tapeItem } from "./recordMedia";

class FakeAudio extends EventTarget {
  src = "";
  currentTime = 0;
  duration = NaN;
  paused = true;
  readyState = 0;
  error = null;
  play() {
    this.paused = false;
    return Promise.resolve();
  }
  pause() {
    this.paused = true;
  }
  load() {}
  emit(type: string) {
    this.dispatchEvent(new Event(type));
  }
}

class FakeCtx {
  state = "suspended" as "suspended" | "running";
  currentTime = 7;
  destination = { kind: "destination" };
  gains: FakeGain[] = [];
  sources = 0;
  resumes = 0;
  createGain() {
    const g = new FakeGain();
    this.gains.push(g);
    return g as unknown as GainNode;
  }
  createMediaElementSource() {
    this.sources++;
    return { connect: (n: unknown) => n };
  }
  resume() {
    this.resumes++;
    this.state = "running";
    return Promise.resolve();
  }
}
class FakeGain {
  sets: [number, number][] = [];
  gain = { setValueAtTime: (v: number, t: number) => void this.sets.push([v, t]) };
  connect() {}
}

const T0 = Date.parse("2026-01-01T00:00:00Z");
const links = (id: string): MediaLinks => ({
  track_id: id,
  expires_at: new Date(T0 + 12 * 3600_000).toISOString(),
  ttl_s: 12 * 3600,
  cover: null,
  lossy: false,
  flac: { url: `/media/${id}.flac`, bytes: 1, content_type: "audio/flac", sample_rate: 44100, bits_per_sample: 16, sha256: "x" },
  mp3: { url: `/media/${id}.mp3`, bytes: 1, content_type: "audio/mpeg", bitrate_kbps: 192, sha256: "y" },
});

function setup(o: { ctx?: FakeCtx | "throw"; quality?: string; rebuild?: (href: string) => Promise<Record<string, number> | null> } = {}) {
  const el = new FakeAudio();
  const ctx = o.ctx === "throw" ? null : (o.ctx ?? new FakeCtx());
  let made = 0;
  const device: DeviceHooks = { quality: () => o.quality ?? "lossless", connection: () => undefined, recordFailedProbe() {} };
  const p = new Player({
    fetchLinks: async (id) => links(id),
    device,
    now: () => T0,
    fetchMeta: async (id) => ({ title: id, album: "" }),
    audioContext: () => {
      made++;
      if (!ctx) throw new Error("no web audio");
      return ctx as unknown as AudioContext;
    },
    ...(o.rebuild ? { rebuildGains: o.rebuild } : {}),
  });
  p.attach(el as unknown as HTMLAudioElement);
  const flush = () => new Promise((r) => setTimeout(r, 0));
  return { p, el, ctx, made: () => made, flush };
}

const lin = (db: number) => Math.pow(10, db / 20);

describe("gain stage", () => {
  test("no gain map: no context, no node, the element is not routed", async () => {
    const { p, ctx, made, flush } = setup();
    p.play("a", ["a", "b"]);
    await flush();
    expect(made()).toBe(0);
    expect(ctx!.sources).toBe(0);
  });

  test("one GainNode for the whole queue, value set at each track start", async () => {
    const { p, el, ctx, flush } = setup();
    p.play("a", ["a", "b", "c"], { gains: { a: -3, c: 2 } });
    await flush();
    expect(ctx!.gains).toHaveLength(1);
    expect(ctx!.sources).toBe(1);
    const g = ctx!.gains[0]!;
    expect(g.sets).toEqual([[lin(-3), 7]]);
    // b has no measured gain: unity, not the previous value.
    el.emit("playing");
    p.next();
    await flush();
    p.next();
    await flush();
    expect(ctx!.gains).toHaveLength(1);
    expect(ctx!.sources).toBe(1);
    expect(g.sets.map((s) => s[0])).toEqual([lin(-3), 1, lin(2)]);
  });

  test("a later plain play keeps the node at unity", async () => {
    const { p, ctx, flush } = setup();
    p.play("a", ["a"], { gains: { a: -6 } });
    await flush();
    p.play("x", ["x", "y"]);
    await flush();
    expect(ctx!.gains).toHaveLength(1);
    expect(ctx!.gains[0]!.sets.at(-1)![0]).toBe(1);
    p.play("b", ["b"], { gains: { b: 1 } });
    await flush();
    expect(ctx!.sources).toBe(1);
  });

  test("the stage is built inside play() and the context woken there (iOS gesture)", () => {
    const { p, ctx } = setup();
    p.play("a", ["a"], { gains: { a: -3 } }); // no await: the link fetch has not resolved
    expect(ctx!.gains).toHaveLength(1);
    expect(ctx!.resumes).toBe(1);
    expect(ctx!.state as string).toBe("running");
  });

  test("a suspended context (A/B suspends the shared one) is woken on play and resume", async () => {
    const { p, el, ctx, flush } = setup();
    p.play("a", ["a", "b"], { gains: { a: -3, b: -3 } });
    await flush();
    el.emit("playing");
    ctx!.state = "suspended";
    ctx!.resumes = 0;
    p.pause();
    p.resume();
    expect(ctx!.resumes).toBe(1);
    ctx!.state = "suspended";
    el.emit("play");
    expect(ctx!.resumes).toBe(2);
  });

  test("audio focus: released inside a gesture resumes at once through the same stage", async () => {
    const { p, el, ctx, flush } = setup();
    p.configure({ inGesture: () => true });
    p.play("a", ["a"], { gains: { a: -3 } });
    await flush();
    el.emit("playing");
    const lease = p.acquireFocus("duel");
    expect(el.paused).toBe(true);
    ctx!.state = "suspended"; // the duel's A/B ended and suspended the shared context
    lease.release({ resume: true });
    await flush();
    expect(el.paused).toBe(false);
    expect(ctx!.state as string).toBe("running");
    expect(ctx!.sources).toBe(1);
  });

  test("audio focus: released outside a gesture holds, and the Resume tap wakes the stage", async () => {
    const { p, el, ctx, flush } = setup();
    p.play("a", ["a"], { gains: { a: -3 } });
    await flush();
    el.emit("playing");
    const lease = p.acquireFocus("duel");
    ctx!.state = "suspended";
    lease.release({ resume: true });
    await flush();
    expect(p.state.held).toBe(true);
    expect(ctx!.state as string).toBe("suspended");
    p.resume();
    expect(p.state.held).toBe(false);
    expect(ctx!.state as string).toBe("running");
    expect(ctx!.gains).toHaveLength(1);
  });

  test("no Web Audio: plays unmatched, no error", async () => {
    const { p, el, flush } = setup({ ctx: "throw" });
    p.play("a", ["a", "b"], { gains: { a: -3 } });
    await flush();
    expect(el.src).toBe("/media/a.flac");
    expect(p.state.error).toBe("");
  });

  test("the gain map is not persisted: the saved shape is unchanged", async () => {
    const { p, flush } = setup();
    p.play("a", ["a", "b"], { gains: { a: -3 }, source: { label: "R", href: "/records/r1" } });
    await flush();
    const snap = p.snapshot();
    expect(Object.keys(snap).sort()).toEqual(["index", "position", "queue", "queuedNext", "source", "trackId"]);
    expect(Object.keys(JSON.parse(serialize(snap, T0)!)).sort()).toEqual(["index", "position", "queue", "queuedNext", "savedAt", "source", "trackId", "v"]);
  });

  test("Undo of a replaced queue puts the gains back", async () => {
    const { p, ctx, flush } = setup();
    p.play("a", ["a", "b"], { gains: { a: -3, b: -4 } });
    await flush();
    const prev = p.snapshotQueue();
    p.play("x", ["x"], { undo: true });
    await flush();
    p.restoreQueue(prev);
    await flush();
    expect(ctx!.gains[0]!.sets.at(-1)![0]).toBe(lin(-3));
  });
});

describe("restored Record queue", () => {
  const saved = (href: string) => ({ v: 1 as const, queue: ["a", "b"], index: 0, queuedNext: 0, position: 12, trackId: "a", source: { label: "R", href }, savedAt: T0 });

  test("a /records/:id source rebuilds the map; the resume tap builds the stage and applies the gain", async () => {
    const asked: string[] = [];
    const { p, ctx, made, flush } = setup({ rebuild: async (h) => (asked.push(h), { a: -5 }) });
    expect(p.restore(saved("/records/r1"))).toBe(true);
    await flush();
    expect(asked).toEqual(["/records/r1"]);
    expect(made()).toBe(0); // boot: no gesture, no context
    p.resume();
    await flush();
    expect(ctx!.gains[0]!.sets.at(-1)![0]).toBe(lin(-5));
  });

  test("any other source asks for nothing", async () => {
    const asked: string[] = [];
    const { p, flush } = setup({ rebuild: async (h) => (asked.push(h), {}) });
    p.restore(saved("/library?x=1"));
    await flush();
    expect(asked).toEqual([]);
  });

  test("a failed rebuild leaves the queue plain", async () => {
    const { p, made, flush } = setup({ rebuild: async () => null });
    p.restore(saved("/records/r1"));
    await flush();
    p.resume();
    await flush();
    expect(made()).toBe(0);
  });
});

describe("tape item", () => {
  test("plays the FLAC link even on a data-saver device and never falls back to the MP3", async () => {
    const { p, el, flush } = setup({ quality: "high" });
    const id = tapeItem("t1");
    p.play(id, [id]);
    await flush();
    expect(el.src).toBe(`/media/${id}.flac`);
    expect(p.state.format).toBe("flac");
    (el as unknown as { error: unknown }).error = { code: 4, message: "" };
    el.dispatchEvent(new Event("error"));
    await flush();
    expect(el.src).toBe(`/media/${id}.flac`);
    expect(p.state.error).toBe("Playback error");
  });

  test("a tape item is not saved", () => {
    const id = tapeItem("t1");
    expect(serialize({ queue: [id], index: 0, queuedNext: 0, position: 3, trackId: id, source: null }, T0)).toBeNull();
    const s = serialize({ queue: ["a", id], index: 0, queuedNext: 0, position: 3, trackId: "a", source: null }, T0)!;
    expect(JSON.parse(s).queue).toEqual(["a"]);
  });
});
