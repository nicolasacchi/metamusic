// Pure helpers of RangeSelect: a [from, to] range in seconds on a 0.1 s grid, with the engine's limits.
export const STEP = 0.1;
const tenth = (n: number) => Math.round(Number((n * 10).toPrecision(12))) / 10;

export interface Range {
  from: number;
  to: number;
}
export interface Limits {
  /** 0 = no limit */
  min: number;
  max: number;
}

export const snap = (s: number) => tenth(s);
export const clampTo = (s: number, duration: number) => tenth(Math.min(Math.max(s, 0), duration > 0 ? duration : s));

/** Text to seconds (0.1 resolution), or null when it is not a number. */
export function parseField(text: string): number | null {
  const t = text.trim();
  if (!/^\d{1,5}(\.\d*)?$/.test(t)) return null;
  return tenth(Number(t));
}

/** Move one end by `delta`, keeping from < to by at least one step and both inside the take. */
export function nudge(r: Range, end: "from" | "to", delta: number, duration: number): Range {
  if (end === "from") return { from: tenth(Math.min(Math.max(r.from + delta, 0), r.to - STEP)), to: r.to };
  return { from: r.from, to: tenth(Math.max(Math.min(r.to + delta, duration > 0 ? duration : r.to + delta), r.from + STEP)) };
}

/** Arrow keys: 0.1 s, with shift 1 s. Null for other keys. */
export function keyDelta(key: string, shift: boolean): number | null {
  const d = shift ? 1 : STEP;
  if (key === "ArrowLeft" || key === "ArrowDown") return -d;
  if (key === "ArrowRight" || key === "ArrowUp") return d;
  return null;
}

/** The pixel x to seconds on a track of `width` px. */
export const xToSeconds = (x: number, width: number, duration: number) => (width > 0 ? clampTo((x / width) * duration, duration) : 0);

/** "" when the range fits the limits, else the message. */
export function rangeProblem(r: Range, lim: Limits): string {
  const span = tenth(r.to - r.from);
  if (!(span > 0)) return "The range must end after it starts.";
  if (lim.min > 0 && span < lim.min) return `The range must be at least ${lim.min} s (it is ${span} s).`;
  if (lim.max > 0 && span > lim.max) return `The range must be at most ${lim.max} s (it is ${span} s).`;
  return "";
}

/** A drag from `a` to `b` (either order) as a range. */
export function dragRange(a: number, b: number): Range {
  return { from: Math.min(a, b), to: Math.max(a, b) };
}
