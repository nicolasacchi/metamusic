import { describe, expect, test } from "bun:test";
import { formatAt, pinPositions, sectionChip, sortComments, type CommentLike } from "./comments";

function c(id: string, at: number | null, created: string, extra: Partial<CommentLike> = {}): CommentLike {
  return { id, at_second: at, created_at: created, kind: "note", section: null, ...extra };
}

describe("comments", () => {
  test("formatAt", () => {
    expect(formatAt(72.04)).toBe("1:12");
    expect(formatAt(0)).toBe("0:00");
  });
  test("pinPositions maps time to x", () => {
    const pins = pinPositions([c("a", 60, "1"), c("b", null, "2")], 120, 300);
    expect(pins).toEqual([{ id: "a", t: 60, kind: "note", x: 150 }]);
  });
  test("pinPositions clamps and survives a zero duration", () => {
    expect(pinPositions([c("a", 500, "1")], 120, 300)[0]!.x).toBe(300);
    expect(pinPositions([c("a", 5, "1")], 0, 300)).toEqual([]);
  });
  test("time order puts untimed comments last", () => {
    const out = sortComments([c("u", null, "3"), c("b", 90, "2"), c("a", 10, "1")], "time");
    expect(out.map((x) => x.id)).toEqual(["a", "b", "u"]);
  });
  test("newest order sorts by created_at descending", () => {
    const out = sortComments([c("a", 10, "2026-01-01"), c("b", null, "2026-01-03"), c("c", 5, "2026-01-02")], "newest");
    expect(out.map((x) => x.id)).toEqual(["b", "c", "a"]);
  });
  test("sectionChip", () => {
    expect(sectionChip(c("a", 1, "1", { section: { label: "chorus 1", confidence: 0.7 } }))).toBe("chorus 1 · ~70%");
    expect(sectionChip(c("a", 1, "1"))).toBeNull();
  });
});
