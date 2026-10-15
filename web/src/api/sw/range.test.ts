import { describe, expect, test } from "bun:test";
import { clampToCap, parseRange, MAX_RANGE } from "./range";

const MiB = 1024 * 1024;

describe("parseRange", () => {
  test("explicit start-end", () => {
    expect(parseRange("bytes=0-1", 1000)).toEqual({ start: 0, end: 1 });
  });
  test("open end", () => {
    expect(parseRange("bytes=100-", 1000)).toEqual({ start: 100, end: 999 });
  });
  test("suffix", () => {
    expect(parseRange("bytes=-500", 1000)).toEqual({ start: 500, end: 999 });
    expect(parseRange("bytes=-5000", 1000)).toEqual({ start: 0, end: 999 });
  });
  test("end past size is clamped to size - 1", () => {
    expect(parseRange("bytes=900-5000", 1000)).toEqual({ start: 900, end: 999 });
  });
  test("bytes=0- on 9 MiB is capped to 4 MiB", () => {
    const r = parseRange("bytes=0-", 9 * MiB);
    expect(r).toEqual({ start: 0, end: 9 * MiB - 1 });
    expect(clampToCap(r as { start: number; end: number }, MAX_RANGE)).toEqual({ start: 0, end: 4 * MiB - 1 });
  });
  test("clampToCap keeps small ranges", () => {
    expect(clampToCap({ start: 10, end: 20 }, MAX_RANGE)).toEqual({ start: 10, end: 20 });
  });
  test("malformed or absent header is null (serve the whole body)", () => {
    expect(parseRange("bytes=abc", 1000)).toBeNull();
    expect(parseRange(null, 1000)).toBeNull();
    expect(parseRange("items=0-1", 1000)).toBeNull();
    expect(parseRange("bytes=0-1,5-6", 1000)).toBeNull();
    expect(parseRange("bytes=5-2", 1000)).toBeNull();
    expect(parseRange("bytes=-", 1000)).toBeNull();
  });
  test("start at or past size is unsatisfiable", () => {
    expect(parseRange("bytes=1000-", 1000)).toBe("unsatisfiable");
    expect(parseRange("bytes=2000-3000", 1000)).toBe("unsatisfiable");
    expect(parseRange("bytes=-0", 1000)).toBe("unsatisfiable");
    expect(parseRange("bytes=0-", 0)).toBe("unsatisfiable");
  });
});
