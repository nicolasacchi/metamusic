// Pure chunk arithmetic: files are stored as 2 MiB chunks (every chunk full except the last).
export const CHUNK = 2 * 1024 * 1024;

export function chunkCount(bytes: number): number {
  return Math.ceil(bytes / CHUNK);
}

/** Indexes of the chunks covering the inclusive byte range. */
export function chunksFor(start: number, end: number): number[] {
  const first = Math.floor(start / CHUNK);
  const last = Math.floor(end / CHUNK);
  const out: number[] = [];
  for (let i = first; i <= last; i++) out.push(i);
  return out;
}

/** `parts[i]` is chunk `floor(start / CHUNK) + i`; returns bytes start..end inclusive. */
export function assemble(parts: Uint8Array[], start: number, end: number): Uint8Array {
  const base = Math.floor(start / CHUNK) * CHUNK;
  const out = new Uint8Array(end - start + 1);
  let at = 0;
  parts.forEach((p, i) => {
    const from = Math.max(start, base + i * CHUNK) - (base + i * CHUNK);
    const to = Math.min(end, base + i * CHUNK + p.length - 1) - (base + i * CHUNK);
    if (to < from) return;
    out.set(p.subarray(from, to + 1), at);
    at += to - from + 1;
  });
  return out;
}
