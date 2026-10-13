import { describe, expect, test } from "bun:test";
import { comparison } from "./fixtures/m2b";
import { compareView } from "./compareView";

describe("compareView", () => {
  test("relation label for siblings", () => {
    expect(compareView(comparison).relation).toBe("siblings");
  });
  test("delta rows carry the sign and a tone", () => {
    const rows = Object.fromEntries(compareView(comparison).deltas.map((r) => [r.key, r]));
    expect(rows["integrated_lufs"]!.sign).toBe("neg");
    expect(rows["integrated_lufs"]!.text).toBe("-2.1 LU");
    expect(rows["duration_s"]!.sign).toBe("pos");
    expect(rows["duration_s"]!.text).toBe("+5.3 s");
    // less clipping in b than in a is good news for b
    expect(rows["clipped_s"]!.sign).toBe("neg");
    expect(rows["clipped_s"]!.tone).toBe("good");
    expect(rows["internal_silence_s"]!.sign).toBe("zero");
    expect(rows["internal_silence_s"]!.tone).toBe("neutral");
    expect(rows["token_loop_span_s"]!.sign).toBe("none");
    expect(rows["token_loop_span_s"]!.text).toBe("–");
  });
  test("flag diff lists only-a, only-b and shared", () => {
    const f = compareView(comparison).flags;
    expect(f.onlyA).toEqual(["clipping"]);
    expect(f.onlyB).toEqual([]);
    expect(f.both).toEqual([]);
  });
  test("recipe diff names the changed fields and seeds", () => {
    const r = compareView(comparison).recipe;
    expect(r.sameHash).toBe(false);
    expect(r.changed).toEqual(["seed"]);
    expect(r.seed).toBe("2001 → 2002");
  });
  test("head-to-head and ladder line", () => {
    const v = compareView(comparison);
    expect(v.headToHead.human).toContain("1-3-1");
    expect(v.headToHead.blindN).toBe(4);
    expect(v.ladderLine).toContain("22%");
  });
  test("every relation has a label", () => {
    for (const rel of ["siblings", "parent_child", "child_parent", "same_lineage", "unrelated"] as const) {
      expect(compareView({ ...comparison, relation: rel }).relation).toBeTruthy();
    }
  });
});
