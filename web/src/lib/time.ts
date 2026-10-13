/** Seconds -> "m:ss". Non-finite or negative input renders as 0:00. */
export function fmt(seconds: number): string {
  const s = Number.isFinite(seconds) && seconds > 0 ? Math.floor(seconds) : 0;
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** Parses "72", "72.0", "1:12" or "1:12.5" into seconds; null when malformed. */
export function parseT(v: string): number | null {
  const m = /^(?:(\d+):([0-5]?\d(?:\.\d+)?)|(\d+(?:\.\d+)?))$/.exec(v.trim());
  if (!m) return null;
  return m[3] !== undefined ? Number(m[3]) : Number(m[1]) * 60 + Number(m[2]);
}
