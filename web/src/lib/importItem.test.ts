import { expect, test } from "bun:test";
import { describeItem, summaryCounts } from "./importItem";

test("describeItem renders objects as key: value text and skips empties", () => {
  expect(describeItem({ code: "X", message: "m", extra: null, n: 0, o: { a: 1 }, e: "" })).toBe('code: X · message: m · n: 0 · o: {"a":1}');
  expect(describeItem("plain")).toBe("plain");
  expect(describeItem(null)).toBe("");
});

test("describeItem keeps markup as literal text", () => {
  expect(describeItem({ line: "<b>&" })).toBe("line: <b>&");
});

test("summaryCounts keeps numeric entries only, sorted", () => {
  expect(summaryCounts({ tracks: 3, audio: 5, note: "x", nested: { a: 1 } })).toEqual([
    ["audio", 5],
    ["tracks", 3],
  ]);
  expect(summaryCounts(null)).toEqual([]);
});
