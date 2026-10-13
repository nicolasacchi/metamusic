import { describe, expect, test } from "bun:test";
import { CROSSFADE_S, START_LEAD_S, ClipLengthMismatch, createAB, playGesture, type AudioBufferLike, type AudioParamLike, type BaseAudioContextLike } from "./ab";

type Call = [string, ...unknown[]];

class FakeGain {
  private v = 1;
  private ramp: { t0: number; v0: number; t1: number; v1: number } | null = null;
  readonly gain: AudioParamLike;
  constructor(
    private ctx: FakeCtx,
    readonly id: string,
  ) {
    const param = {
      cancelScheduledValues: (t: number) => {
        this.ctx.log.push([`${this.id}.cancel`, t]);
        // Cancelling drops the ramp; the value falls back to the base value, as in browsers.
        this.v = this.current();
        this.ramp = null;
      },
      setValueAtTime: (v: number, t: number) => {
        this.ctx.log.push([`${this.id}.set`, v, t]);
        this.v = v;
        this.ramp = null;
      },
      linearRampToValueAtTime: (v: number, t: number) => {
        this.ctx.log.push([`${this.id}.ramp`, v, t]);
        this.ramp = { t0: this.ctx.currentTime, v0: this.current(), t1: t, v1: v };
      },
    } as AudioParamLike;
    Object.defineProperty(param, "value", {
      get: () => this.current(),
      set: (x: number) => {
        this.v = x;
        this.ramp = null;
      },
    });
    this.gain = param;
  }
  private current(): number {
    const r = this.ramp;
    if (r && this.ctx.currentTime >= r.t0) {
      const f = Math.min(1, (this.ctx.currentTime - r.t0) / (r.t1 - r.t0));
      return r.v0 + (r.v1 - r.v0) * f;
    }
    return this.v;
  }
  connect(n: unknown) {
    this.ctx.log.push([`${this.id}.connect`, n === this.ctx.destination ? "destination" : "?"]);
  }
  disconnect() {
    this.ctx.log.push([`${this.id}.disconnect`]);
  }
}

class FakeSource {
  buffer: unknown = null;
  loop = false;
  loopStart = 0;
  loopEnd = 0;
  constructor(
    private ctx: FakeCtx,
    readonly id: string,
  ) {}
  connect(n: FakeGain) {
    this.ctx.log.push([`${this.id}.connect`, n.id]);
  }
  disconnect() {
    this.ctx.log.push([`${this.id}.disconnect`]);
  }
  start(when: number, offset: number) {
    this.ctx.log.push([`${this.id}.start`, when, offset, this.loop, this.loopStart, this.loopEnd]);
  }
  stop() {
    this.ctx.log.push([`${this.id}.stop`]);
  }
}

class FakeCtx {
  currentTime = 10;
  destination = { kind: "dest" };
  log: Call[] = [];
  private n = 0;
  createBufferSource() {
    return new FakeSource(this, "src" + ++this.n);
  }
  createGain() {
    return new FakeGain(this, "gain" + ++this.n);
  }
  suspend() {
    this.log.push(["suspend"]);
    return Promise.resolve();
  }
  resume() {
    this.log.push(["resume"]);
    return Promise.resolve();
  }
  names() {
    return this.log.map((c) => c[0]);
  }
  find(name: string) {
    return this.log.filter((c) => c[0] === name);
  }
}

const buf = (duration: number): AudioBufferLike => ({ duration });
const mk = (da = 30, db = 30) => {
  const ctx = new FakeCtx();
  return { ctx, ab: createAB(ctx as unknown as BaseAudioContextLike, buf(da), buf(db)) };
};

describe("createAB", () => {
  test("constants", () => {
    expect(CROSSFADE_S).toBe(0.008);
    expect(START_LEAD_S).toBe(0.05);
  });

  test("both starts receive identical (when, offset); sources loop to min duration", () => {
    const { ctx, ab } = mk(30, 30.0005);
    ab.play(3);
    const all = ctx.log.filter((c) => c[0].endsWith(".start"));
    expect(all).toHaveLength(2);
    expect(all[0]!.slice(1)).toEqual(all[1]!.slice(1));
    expect(all[0]![1]).toBeCloseTo(10 + START_LEAD_S, 9);
    expect(all[0]![2]).toBe(3);
    expect(all[0]![3]).toBe(true);
    expect(all[0]![4]).toBe(0);
    expect(all[0]![5]).toBe(30);
    expect(ab.playing).toBe(true);
  });

  test("durations differing by more than 1 ms throw ClipLengthMismatch", () => {
    expect(() => mk(30.0, 30.002)).toThrow(ClipLengthMismatch);
    expect(() => mk(30.002, 30.0)).toThrow(ClipLengthMismatch);
    expect(() => mk(30.0, 30.0009)).not.toThrow();
  });

  test("select(b) ramps A to 0 and B to 1 after cancel + setValueAtTime", () => {
    const { ctx, ab } = mk();
    ab.play();
    ctx.log.length = 0;
    ctx.currentTime = 12;
    ab.select("b");
    const names = ctx.names();
    expect(names.filter((n) => n.endsWith(".cancel"))).toHaveLength(2);
    const ramps = ctx.log.filter((c) => c[0].endsWith(".ramp"));
    expect(ramps).toHaveLength(2);
    const gainA = ctx.log.find((c) => c[0].endsWith(".ramp") && c[1] === 0)!;
    const gainB = ctx.log.find((c) => c[0].endsWith(".ramp") && c[1] === 1)!;
    expect(gainA[2]).toBeCloseTo(12 + CROSSFADE_S, 9);
    expect(gainB[2]).toBeCloseTo(12 + CROSSFADE_S, 9);
    const aId = (gainA[0] as string).split(".")[0];
    const bId = (gainB[0] as string).split(".")[0];
    expect(ctx.log.find((c) => c[0] === aId + ".set")).toEqual([aId + ".set", 1, 12]);
    expect(ctx.log.find((c) => c[0] === bId + ".set")).toEqual([bId + ".set", 0, 12]);
    // cancel precedes set precedes ramp for the outgoing gain
    const idx = (n: string) => names.indexOf(n);
    expect(idx(aId + ".cancel")).toBeLessThan(idx(aId + ".set"));
    expect(idx(aId + ".set")).toBeLessThan(idx(aId + ".ramp"));
    expect(ab.active).toBe("b");
    expect(ab.switches).toBe(1);
  });

  test("select of the active slot is a no-op", () => {
    const { ctx, ab } = mk();
    ab.play();
    ctx.log.length = 0;
    ab.select("a");
    expect(ctx.log).toHaveLength(0);
    expect(ab.switches).toBe(0);
  });

  test("toggling twice within 8 ms cancels and re-ramps from the current value", () => {
    const { ctx, ab } = mk();
    ab.play();
    ctx.currentTime = 12;
    ab.toggle(); // a -> b
    ctx.log.length = 0;
    ctx.currentTime = 12.004; // half way
    ab.toggle(); // b -> a
    const sets = ctx.log.filter((c) => c[0].endsWith(".set"));
    expect(sets).toHaveLength(2);
    const values = sets.map((c) => c[1] as number).sort();
    expect(values[0]).toBeCloseTo(0.5, 6);
    expect(values[1]).toBeCloseTo(0.5, 6);
    for (const s of sets) expect(s[2]).toBe(12.004);
    expect(ctx.names().filter((n) => n.endsWith(".cancel"))).toHaveLength(2);
    expect(ab.active).toBe("a");
    expect(ab.switches).toBe(2);
  });

  test("seek stops the old pair and starts a new pair with identical args", () => {
    const { ctx, ab } = mk();
    ab.play(1);
    ctx.currentTime = 11;
    ctx.log.length = 0;
    ab.seek(12);
    expect(ctx.names().filter((n) => n.endsWith(".stop"))).toHaveLength(2);
    const starts = ctx.log.filter((c) => c[0].endsWith(".start"));
    expect(starts).toHaveLength(2);
    expect(starts[0]!.slice(1)).toEqual(starts[1]!.slice(1));
    expect(starts[0]![1]).toBeCloseTo(11 + START_LEAD_S, 9);
    expect(starts[0]![2]).toBe(12);
    // the active slot keeps gain 1 on the new pair
    ab.select("b");
    expect(ab.active).toBe("b");
  });

  test("seek offset beyond the loop wraps into it", () => {
    const { ctx, ab } = mk(30, 30);
    ab.play();
    ctx.log.length = 0;
    ab.seek(65);
    const starts = ctx.log.filter((c) => c[0].endsWith(".start"));
    expect(starts[0]![2]).toBe(5);
  });

  test("position follows the clock within the loop", () => {
    const { ctx, ab } = mk(30, 30);
    ab.play(5);
    expect(ab.position()).toBe(5);
    ctx.currentTime = 10 + START_LEAD_S + 4;
    expect(ab.position()).toBeCloseTo(9, 6);
    ctx.currentTime = 10 + START_LEAD_S + 28;
    expect(ab.position()).toBeCloseTo(3, 6);
  });

  test("pause and resume call suspend and resume", async () => {
    const { ctx, ab } = mk();
    ab.play();
    ctx.log.length = 0;
    await ab.pause();
    expect(ctx.names()).toEqual(["suspend"]);
    await ab.resume();
    expect(ctx.names()).toEqual(["suspend", "resume"]);
  });

  test("dispose stops and disconnects both and is idempotent", () => {
    const { ctx, ab } = mk();
    ab.play();
    ctx.log.length = 0;
    ab.dispose();
    expect(ctx.names().filter((n) => n.endsWith(".stop"))).toHaveLength(2);
    expect(ctx.names().filter((n) => n.endsWith(".disconnect")).length).toBeGreaterThanOrEqual(2);
    expect(ab.playing).toBe(false);
    ctx.log.length = 0;
    ab.dispose();
    expect(ctx.log).toHaveLength(0);
  });

  test("playGesture calls ctx.resume() synchronously, before play() and before any await", async () => {
    const { ctx, ab } = mk();
    const p = playGesture(ab, 2);
    // nothing awaited yet: the resume is already recorded, ahead of the source starts
    expect(ctx.names()[0]).toBe("resume");
    expect(ctx.names().some((n) => n.endsWith(".start"))).toBe(true);
    await p;
  });
});
