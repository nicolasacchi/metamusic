// The A/B window of a repaint child against its parent (C149.5). A duel window is the clip window: 30 to 45 s on a
// 100 ms grid. A shorter range is widened to 30 s around its midpoint (clamped into the take); a range of 30 to 45 s
// is used as it is; a longer one keeps its first 45 s and is flagged `truncated` (the server's ab_window_truncated).

export const AB_MIN_S = 30;
export const AB_MAX_S = 45;

export interface AbWindow {
  from_s: number;
  to_s: number;
  /** The range was longer than the longest window. */
  truncated: boolean;
}

const tenth = (n: number) => Math.round(Number((n * 10).toPrecision(12))) / 10;

export function abWindow(fromS: number, toS: number, durationS: number): AbWindow {
  const from = tenth(fromS);
  const to = tenth(toS);
  const span = to - from;
  if (span > AB_MAX_S) return { from_s: from, to_s: tenth(from + AB_MAX_S), truncated: true };
  if (span >= AB_MIN_S) return { from_s: from, to_s: to, truncated: false };
  const dur = durationS > 0 ? durationS : to;
  if (dur <= AB_MIN_S) return { from_s: 0, to_s: tenth(Math.max(dur, 0)), truncated: false };
  const mid = (from + to) / 2;
  const start = tenth(Math.min(Math.max(mid - AB_MIN_S / 2, 0), dur - AB_MIN_S));
  return { from_s: start, to_s: tenth(start + AB_MIN_S), truncated: false };
}
