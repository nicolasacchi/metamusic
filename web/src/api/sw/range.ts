// Pure Range header parsing for the service worker's media responses (C115.1).
export const MAX_RANGE = 4 * 1024 * 1024;

export interface ByteRange {
  /** Inclusive. */
  start: number;
  end: number;
}

/**
 * Single-range `bytes=` headers only. null: absent or malformed (serve the whole body, like an
 * origin that ignores Range); "unsatisfiable": the start is at or past the end of the file (416).
 */
export function parseRange(header: string | null, size: number): ByteRange | "unsatisfiable" | null {
  if (header === null) return null;
  const m = /^bytes=(\d*)-(\d*)$/.exec(header.trim());
  if (!m || (m[1] === "" && m[2] === "")) return null;
  const a = m[1] ?? "";
  const b = m[2] ?? "";
  if (a === "") {
    const n = Number(b);
    if (!Number.isSafeInteger(n) || n === 0 || size === 0) return "unsatisfiable";
    return { start: Math.max(0, size - n), end: size - 1 };
  }
  const start = Number(a);
  if (!Number.isSafeInteger(start)) return null;
  if (b !== "") {
    const end = Number(b);
    if (!Number.isSafeInteger(end) || end < start) return null;
    if (start >= size) return "unsatisfiable";
    return { start, end: Math.min(end, size - 1) };
  }
  if (start >= size) return "unsatisfiable";
  return { start, end: size - 1 };
}

/** At most `cap` bytes from the start of the range. */
export function clampToCap(r: ByteRange, cap: number): ByteRange {
  return { start: r.start, end: Math.min(r.end, r.start + cap - 1) };
}
