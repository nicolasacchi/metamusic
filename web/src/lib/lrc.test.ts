import { describe, expect, test } from "bun:test";
import { lrcActive, parseLrc } from "./lrc";

describe("lrc", () => {
  test("parses stamps, skips tags, sorts, multi-stamp lines", () => {
    const lines = parseLrc("[ar:Someone]\r\n[00:12.50] second\n[00:01.5]first\n[01:00][00:30.25]chorus\nplain\n");
    expect(lines).toEqual([
      { t: 1.5, text: "first" },
      { t: 12.5, text: "second" },
      { t: 30.25, text: "chorus" },
      { t: 60, text: "chorus" },
    ]);
  });
  test("empty and stampless input give no lines", () => {
    expect(parseLrc("")).toEqual([]);
    expect(parseLrc("just words")).toEqual([]);
  });
  test("active line follows the playhead", () => {
    const lines = parseLrc("[00:05.00]a\n[00:10.00]b\n[00:20.00]c");
    expect(lrcActive(lines, 0)).toBe(-1);
    expect(lrcActive(lines, 5)).toBe(0);
    expect(lrcActive(lines, 19.9)).toBe(1);
    expect(lrcActive(lines, 999)).toBe(2);
    expect(lrcActive([], 3)).toBe(-1);
  });
});
