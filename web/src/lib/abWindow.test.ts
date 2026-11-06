import { describe, expect, test } from "bun:test";
import { abWindow } from "./abWindow";

const rows: [string, number, number, number, [number, number, boolean]][] = [
  ["short range near the start clamps to 0", 8, 14, 100, [0, 30, false]],
  ["short range in the middle is centred", 50, 56, 100, [38, 68, false]],
  ["range at the end shifts back to keep 30 s", 94, 100, 100, [70, 100, false]],
  ["range of exactly 30 s is itself", 10, 40, 100, [10, 40, false]],
  ["40 s range is itself", 20, 60, 100, [20, 60, false]],
  ["45 s range is itself", 0, 45, 100, [0, 45, false]],
  ["80 s range keeps its first 45 s and is flagged", 10, 90, 120, [10, 55, true]],
  ["take shorter than 30 s uses the whole take", 3, 6, 20, [0, 20, false]],
  ["rounds to the 100 ms grid", 12.34, 20.06, 100, [1.2, 31.2, false]],
];
describe("abWindow", () => {
  for (const [name, a, b, d, [f, t, tr]] of rows) {
    test(name, () => expect(abWindow(a, b, d)).toEqual({ from_s: f, to_s: t, truncated: tr }));
  }
});
