// A/B engine (C54.1): two looping AudioBufferSourceNodes on one AudioContext clock, started with
// identical (when, offset) arguments; switching is an 8 ms gain crossfade. DOM-free: the context is
// injected so tests record every call.
export const CROSSFADE_S = 0.008;
export const START_LEAD_S = 0.05;
const MAX_LENGTH_DELTA_S = 0.001;

export type Slot = "a" | "b";

export interface AudioParamLike {
  value: number;
  cancelScheduledValues(t: number): unknown;
  setValueAtTime(v: number, t: number): unknown;
  linearRampToValueAtTime(v: number, t: number): unknown;
}
export interface GainNodeLike {
  gain: AudioParamLike;
  connect(n: unknown): unknown;
  disconnect(): void;
}
export interface AudioBufferLike {
  readonly duration: number;
}
export interface SourceNodeLike {
  buffer: unknown;
  loop: boolean;
  loopStart: number;
  loopEnd: number;
  connect(n: unknown): unknown;
  disconnect(): void;
  start(when: number, offset: number): void;
  stop(): void;
}
export interface BaseAudioContextLike {
  readonly currentTime: number;
  readonly destination: unknown;
  createBufferSource(): SourceNodeLike;
  createGain(): GainNodeLike;
  suspend(): Promise<void>;
  resume(): Promise<void>;
}

export class ClipLengthMismatch extends Error {
  constructor(a: number, b: number) {
    super(`Clip lengths differ (${a.toFixed(3)} s vs ${b.toFixed(3)} s)`);
    this.name = "ClipLengthMismatch";
  }
}

export interface AB {
  readonly active: Slot;
  readonly playing: boolean;
  readonly switches: number;
  /** Start both sources. */
  play(offsetS?: number): void;
  /** ctx.suspend(): both stop on the same clock. */
  pause(): Promise<void>;
  /** ctx.resume(). */
  resume(): Promise<void>;
  select(s: Slot): void;
  toggle(): void;
  /** Restart both at the same when/offset. */
  seek(offsetS: number): void;
  /** Seconds within the loop. */
  position(): number;
  /** Stop and disconnect the sources; the shared context stays open. */
  dispose(): void;
}

interface Voice {
  src: SourceNodeLike;
  gain: GainNodeLike;
}

export function createAB(ctx: BaseAudioContextLike, a: AudioBufferLike, b: AudioBufferLike): AB {
  if (Math.abs(a.duration - b.duration) > MAX_LENGTH_DELTA_S) throw new ClipLengthMismatch(a.duration, b.duration);
  const loopEnd = Math.min(a.duration, b.duration);
  const buffers: Record<Slot, AudioBufferLike> = { a, b };
  let active: Slot = "a";
  let switches = 0;
  let voices: Record<Slot, Voice> | null = null;
  let started = false;
  let startWhen = 0;
  let startOffset = 0;

  const wrap = (o: number) => (loopEnd > 0 && Number.isFinite(o) ? ((o % loopEnd) + loopEnd) % loopEnd : 0);

  function stopVoices() {
    if (!voices) return;
    for (const v of [voices.a, voices.b]) {
      try {
        v.src.stop();
      } catch {
        /* already stopped */
      }
      v.src.disconnect();
      v.gain.disconnect();
    }
    voices = null;
  }

  function voice(s: Slot): Voice {
    const src = ctx.createBufferSource();
    src.buffer = buffers[s];
    src.loop = true;
    src.loopStart = 0;
    src.loopEnd = loopEnd;
    const gain = ctx.createGain();
    gain.gain.value = s === active ? 1 : 0;
    src.connect(gain);
    gain.connect(ctx.destination);
    return { src, gain };
  }

  function startPair(offset: number) {
    stopVoices();
    startOffset = wrap(offset);
    startWhen = ctx.currentTime + START_LEAD_S;
    const pair = { a: voice("a"), b: voice("b") };
    voices = pair;
    // Identical arguments for both sources: this is what keeps them sample-aligned.
    pair.a.src.start(startWhen, startOffset);
    pair.b.src.start(startWhen, startOffset);
    started = true;
  }

  return {
    get active() {
      return active;
    },
    get playing() {
      return started;
    },
    get switches() {
      return switches;
    },
    play(offsetS = 0) {
      startPair(offsetS);
    },
    pause() {
      return ctx.suspend();
    },
    resume() {
      return ctx.resume();
    },
    select(s) {
      if (s === active) return;
      active = s;
      switches++;
      if (!voices) return;
      const t = ctx.currentTime;
      const incoming = voices[s].gain.gain;
      const outgoing = voices[s === "a" ? "b" : "a"].gain.gain;
      for (const [p, target] of [
        [outgoing, 0],
        [incoming, 1],
      ] as const) {
        // Read the live value first: cancelScheduledValues drops the in-flight ramp. No
        // cancelAndHoldAtTime (Firefox lacks it).
        const v = p.value;
        p.cancelScheduledValues(t);
        p.setValueAtTime(v, t);
        p.linearRampToValueAtTime(target, t + CROSSFADE_S);
      }
    },
    toggle() {
      this.select(active === "a" ? "b" : "a");
    },
    seek(offsetS) {
      if (!started) {
        startOffset = wrap(offsetS);
        return;
      }
      startPair(offsetS);
    },
    position() {
      if (!started) return startOffset;
      const elapsed = Math.max(0, ctx.currentTime - startWhen);
      return wrap(startOffset + elapsed);
    },
    dispose() {
      stopVoices();
      started = false;
    },
  };
}

/**
 * The Play click handler. `ctx.resume()` is called synchronously, before any await (iOS only resumes a
 * context inside the user gesture), then the sources are scheduled.
 */
export function playGesture(ab: AB, offsetS?: number): Promise<void> {
  const resumed = ab.resume();
  ab.play(offsetS);
  return resumed;
}
