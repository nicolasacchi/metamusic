import { describe, expect, test } from "bun:test";
import { sectionAt } from "./sections";

const timed = [
  { start_s: 0, end_s: 4 },
  { start_s: 4, end_s: 8 },
];
describe("sectionAt", () => {
  test("timed boundaries are [start,end)", () => {
    expect(sectionAt(timed, 0)).toBe(0);
    expect(sectionAt(timed, 3.99)).toBe(0);
    expect(sectionAt(timed, 4.0)).toBe(1);
    expect(sectionAt(timed, 8)).toBe(-1);
  });
  test("untimed gives -1", () => {
    expect(sectionAt([{ start_s: null, end_s: null }], 1)).toBe(-1);
    expect(sectionAt([], 1)).toBe(-1);
  });
  test("start-only sections extend to the next start", () => {
    const s = [
      { start_s: 0, end_s: null },
      { start_s: 5, end_s: null },
    ];
    expect(sectionAt(s, 6)).toBe(1);
    expect(sectionAt(s, 2)).toBe(0);
  });
});
