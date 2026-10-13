import { describe, expect, test } from "bun:test";
import { spectroSeek } from "./spectroSeek";

const plot = { x0: 142, y0: 64, width: 1024, height: 384, duration_s: 200, freq_scale: "log" };

describe("spectroSeek", () => {
  test("click at the plot middle on a half-size image is duration/2", () => {
    const natural = 1308;
    const rendered = natural / 2;
    expect(spectroSeek((plot.x0 + plot.width / 2) / 2, rendered, natural, plot)).toBeCloseTo(100, 6);
  });
  test("full size and clamping", () => {
    expect(spectroSeek(plot.x0, 1308, 1308, plot)).toBe(0);
    expect(spectroSeek(plot.x0 + plot.width, 1308, 1308, plot)).toBeCloseTo(200, 6);
    expect(spectroSeek(0, 1308, 1308, plot)).toBe(0);
    expect(spectroSeek(1300, 1308, 1308, plot)).toBe(200);
  });
  test("degenerate sizes give 0", () => {
    expect(spectroSeek(10, 0, 1308, plot)).toBe(0);
    expect(spectroSeek(10, 100, 0, plot)).toBe(0);
    expect(spectroSeek(NaN, 100, 100, plot)).toBe(0);
    expect(spectroSeek(10, 100, 100, { ...plot, width: 0 })).toBe(0);
  });
});
