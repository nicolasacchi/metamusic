// LRC lyrics (E2-W5): parse "[mm:ss.xx] line" files into timed lines and find the line at the playhead.
export interface LrcLine {
  /** Seconds from the start. */
  t: number;
  text: string;
}

const STAMP = /\[(\d{1,3}):(\d{2})(?:[.:](\d{1,3}))?\]/g;

/** Timed lines sorted by time. Metadata tags ([ar:..]) and lines without a stamp are skipped; a line may carry several stamps. */
export function parseLrc(src: string): LrcLine[] {
  const out: LrcLine[] = [];
  for (const raw of src.replace(/^\uFEFF/, "").split(/\r?\n/)) {
    const stamps = [...raw.matchAll(STAMP)];
    if (stamps.length === 0) continue;
    const text = raw.replace(STAMP, "").trim();
    for (const m of stamps) {
      const frac = m[3] ? Number(`0.${m[3]}`) : 0;
      out.push({ t: Number(m[1]) * 60 + Number(m[2]) + frac, text });
    }
  }
  return out.sort((a, b) => a.t - b.t);
}

/** Index of the line being sung at `t` seconds, or -1 before the first line. Lines must be sorted (parseLrc does). */
export function lrcActive(lines: readonly LrcLine[], t: number): number {
  let lo = 0;
  let hi = lines.length - 1;
  let at = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (lines[mid]!.t <= t) {
      at = mid;
      lo = mid + 1;
    } else hi = mid - 1;
  }
  return at;
}
