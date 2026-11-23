// Headset grammar of the Morning Crate (M6N-W2, C384.1). Pure: the clock, the timer and every effect are injected.
//
// One-handed on a phone, a headset has two buttons the lock screen forwards as Media Session actions:
//   previoustrack   one press   = a marker, buffered 3 s; a second press inside that window turns it into ONE
//                                 strong marker at the position of the FIRST press (the buffered marker is dropped)
//   nexttrack       one press   = skip: a `skip` event at the position, then a seek to the start of the next chapter
// The preset `next_marks` swaps the two. A chapter that ends without a skip emits `complete`; entering a chapter emits
// `play` once. Nothing here knows the clock of the audio element: the session feeds positions in.

export type HeadsetAction = "previoustrack" | "nexttrack";
export type HeadsetPreset = "prev_marks" | "next_marks";
export type HeadsetKind = "marker" | "strong_marker" | "skip" | "complete" | "play";

/** Where the listener is: the 0-based side, the position in that side (ms) and the global 1-based chapter number. */
export interface HeadsetPos {
  side: number;
  sideMs: number;
  chapter: number;
}

export interface HeadsetEvent {
  kind: HeadsetKind;
  side: number;
  chapter: number;
  side_ms: number;
  /** Press-to-ear delay a marker position is moved back by; only markers carry it. */
  latency_ms?: number;
}

/** How long a lone marker press waits for a second press before it is committed. */
export const STRONG_WINDOW_MS = 3000;
/** Assumed delay between hearing something and pressing the button (the server clamps the result to the take). */
export const MARKER_LATENCY_MS = 500;

export interface HeadsetDeps {
  position(): HeadsetPos | null;
  emit(e: HeadsetEvent): void;
  /** Move playback to the start of the chapter after the one at `pos` (or the next side, or the end). */
  seekNext(pos: HeadsetPos): void;
  setTimer(fn: () => void, ms: number): unknown;
  clearTimer(h: unknown): void;
  /** Short text for the lock screen title and the page ("Marker 3 @ 1:12"). */
  flash?(text: string): void;
  vibrate?(): void;
  latencyMs?: number;
}

export const mmss = (ms: number): string => {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
};

export class HeadsetGrammar {
  preset: HeadsetPreset;
  private deps: HeadsetDeps;
  private pending: { pos: HeadsetPos; timer: unknown } | null = null;
  private skipped = new Set<number>();
  private done = new Set<number>();
  private played = new Set<number>();
  private cur: number | null = null;

  constructor(deps: HeadsetDeps, preset: HeadsetPreset = "prev_marks") {
    this.deps = deps;
    this.preset = preset;
  }

  /** A Media Session action. */
  press(action: HeadsetAction): void {
    const marks = (action === "previoustrack") === (this.preset === "prev_marks");
    if (marks) this.mark();
    else this.skip();
  }

  /** The marker role: first press buffers, a second press inside the window makes one strong marker. */
  mark(): void {
    const pos = this.deps.position();
    if (!pos) return;
    this.deps.vibrate?.();
    if (this.pending) {
      const first = this.pending.pos;
      this.deps.clearTimer(this.pending.timer);
      this.pending = null;
      this.out({ kind: "strong_marker", ...at(first), latency_ms: this.latency() });
      this.deps.flash?.(`Keep ${first.chapter} @ ${mmss(first.sideMs)}`);
      return;
    }
    const timer = this.deps.setTimer(() => this.flush(), STRONG_WINDOW_MS);
    this.pending = { pos, timer };
    this.deps.flash?.(`Marker ${pos.chapter} @ ${mmss(pos.sideMs)}`);
  }

  /** The skip role: commits a buffered marker first, then skips. */
  skip(): void {
    this.flush();
    const pos = this.deps.position();
    if (!pos) return;
    this.deps.vibrate?.();
    this.skipped.add(pos.chapter);
    this.out({ kind: "skip", ...at(pos) });
    this.deps.flash?.(`Skipped ${pos.chapter}`);
    this.deps.seekNext(pos);
  }

  /** Commits a buffered lone marker now (the window ran out, playback paused, the page is hiding). */
  flush(): void {
    const p = this.pending;
    if (!p) return;
    this.deps.clearTimer(p.timer);
    this.pending = null;
    this.out({ kind: "marker", ...at(p.pos), latency_ms: this.latency() });
  }

  /**
   * The listener is at `pos` (called as playback advances). Entering a chapter emits `play` once; moving forward to
   * the very next chapter by playing on completes the one left, unless it was skipped. `prevEnd` is where the chapter
   * that was left ends.
   */
  observe(pos: HeadsetPos, prevEnd?: HeadsetPos): void {
    if (this.cur === pos.chapter) return;
    const left = this.cur;
    this.cur = pos.chapter;
    if (left !== null && pos.chapter === left + 1) this.complete(left, prevEnd);
    if (!this.played.has(pos.chapter)) {
      this.played.add(pos.chapter);
      this.out({ kind: "play", ...at(pos) });
    }
  }

  /** A side played to its end: its last chapter is complete unless skipped. */
  ended(pos: HeadsetPos): void {
    this.complete(pos.chapter, pos);
  }

  /** The position moved by something that is not playing on (a seek, a skip, going back): no `complete` for what was left. */
  jumped(): void {
    this.cur = null;
  }

  dispose(): void {
    this.flush();
  }

  private complete(chapter: number, end?: HeadsetPos): void {
    if (this.skipped.has(chapter) || this.done.has(chapter)) return;
    const pos = end ?? this.deps.position();
    if (!pos) return;
    this.done.add(chapter);
    this.out({ kind: "complete", ...at({ ...pos, chapter }) });
  }

  private latency(): number {
    return Math.min(5000, Math.max(0, Math.round(this.deps.latencyMs ?? MARKER_LATENCY_MS)));
  }

  private out(e: HeadsetEvent): void {
    this.deps.emit(e);
  }
}

function at(p: HeadsetPos): Pick<HeadsetEvent, "side" | "chapter" | "side_ms"> {
  return { side: p.side, chapter: p.chapter, side_ms: Math.max(0, Math.round(p.sideMs)) };
}
