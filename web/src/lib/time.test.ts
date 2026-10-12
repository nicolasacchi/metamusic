import { describe, expect, test } from "bun:test";
import { fmt, parseT } from "./time";

describe("time", () => {
  test("parseT", () => {
    expect(parseT("72")).toBe(72);
    expect(parseT("1:12")).toBe(72);
    expect(parseT("bad")).toBeNull();
    expect(parseT("")).toBeNull();
    expect(parseT("-3")).toBeNull();
    expect(parseT("1:2:3")).toBeNull();
    expect(parseT("1:75")).toBeNull();
  });
  test("fmt", () => {
    expect(fmt(72)).toBe("1:12");
    expect(fmt(5.9)).toBe("0:05");
    expect(fmt(NaN)).toBe("0:00");
    expect(fmt(-1)).toBe("0:00");
    expect(fmt(3600)).toBe("60:00");
  });
});
