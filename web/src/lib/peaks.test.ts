import { describe, expect, test } from "bun:test";
import { peaksToColumns } from "./peaks";

describe("peaksToColumns", () => {
  test("2048 pairs -> 300 columns takes min of mins and max of maxes", () => {
    const pairs = 2048;
    const data: number[] = [];
    for (let i = 0; i < pairs; i++) data.push(-(i % 100), i % 100);
    const cols = peaksToColumns(data, pairs, 300);
    expect(cols).toHaveLength(300);
    for (let c = 0; c < 300; c++) {
      const a = Math.floor((c * pairs) / 300);
      const b = Math.max(a + 1, Math.floor(((c + 1) * pairs) / 300));
      let mn = Infinity;
      let mx = -Infinity;
      for (let i = a; i < b; i++) {
        mn = Math.min(mn, -(i % 100));
        mx = Math.max(mx, i % 100);
      }
      expect(cols[c]).toEqual({ min: mn, max: mx });
    }
  });
  test("width greater than pairs repeats pairs", () => {
    const cols = peaksToColumns([-1, 1, -2, 2], 2, 4);
    expect(cols).toEqual([
      { min: -1, max: 1 },
      { min: -1, max: 1 },
      { min: -2, max: 2 },
      { min: -2, max: 2 },
    ]);
  });
  test("empty input gives no columns", () => {
    expect(peaksToColumns([], 0, 100)).toEqual([]);
    expect(peaksToColumns([-1, 1], 1, 0)).toEqual([]);
  });
});
