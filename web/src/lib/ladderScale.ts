// Ladder whisker geometry (C54.5): the scale is symmetric around the reject line (u = 0).
const Z95 = 1.96;

export interface UncertainU {
  u: number;
  sd: number;
}

/** Half-range of the scale: covers every whisker, at least 1, rounded up to a multiple of 0.5. */
export function extentFor(items: readonly UncertainU[]): number {
  let m = 1;
  for (const it of items) {
    const reach = Math.abs(it.u) + Z95 * Math.max(0, it.sd);
    if (Number.isFinite(reach)) m = Math.max(m, reach);
  }
  return Math.ceil(m * 2 - 1e-9) / 2;
}

/** Pixel position of u on a [-extent, +extent] scale `width` pixels wide; clamped, u = 0 is the centre. */
export function xFor(u: number, extent: number, width: number): number {
  const e = extent > 0 ? extent : 1;
  const v = Number.isFinite(u) ? Math.min(e, Math.max(-e, u)) : 0;
  return ((v + e) / (2 * e)) * width;
}

export function whisker(it: UncertainU, extent: number, width: number): { x: number; lo: number; hi: number } {
  const half = Z95 * Math.max(0, Number.isFinite(it.sd) ? it.sd : 0);
  return { x: xFor(it.u, extent, width), lo: xFor(it.u - half, extent, width), hi: xFor(it.u + half, extent, width) };
}
