export interface Column {
  min: number;
  max: number;
}

/** Reduce interleaved [min0,max0,min1,max1,...] pairs to `width` drawing columns. */
export function peaksToColumns(data: readonly number[], pairs: number, width: number): Column[] {
  const n = Math.min(pairs, Math.floor(data.length / 2));
  if (n <= 0 || width <= 0) return [];
  const out: Column[] = [];
  for (let c = 0; c < width; c++) {
    const a = Math.min(n - 1, Math.floor((c * n) / width));
    const b = Math.min(n, Math.max(a + 1, Math.floor(((c + 1) * n) / width)));
    let min = Infinity;
    let max = -Infinity;
    for (let i = a; i < b; i++) {
      const lo = data[2 * i]!;
      const hi = data[2 * i + 1]!;
      if (lo < min) min = lo;
      if (hi > max) max = hi;
    }
    out.push({ min, max });
  }
  return out;
}
