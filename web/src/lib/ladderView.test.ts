import { describe, expect, test } from "bun:test";
import { ladderPage } from "./fixtures/m2b";
import { ladderFooter, ladderRows } from "./ladderView";

describe("ladderRows", () => {
  test("keeps the served rank order and formats the record W-L-T-BB", () => {
    const { rows } = ladderRows(ladderPage, 400);
    expect(rows.map((r) => r.rank)).toEqual([1, 2, 3]);
    expect(rows[0]!.title).toBe("Synthetic Beta");
    expect(rows[0]!.record).toBe("4-1-1-0");
    expect(rows[2]!.record).toBe("0-1-0-1");
    expect(rows[2]!.provisional).toBe(true);
    expect(rows[0]!.href).toBe(`/tracks/${ladderPage.items[0]!.track_id}`);
  });
  test("whiskers lie within the scale and the reject line is centred", () => {
    const { rows, extent, zeroX } = ladderRows(ladderPage, 400);
    expect(zeroX).toBe(200);
    expect(extent).toBeGreaterThanOrEqual(1);
    for (const r of rows) {
      expect(r.lo).toBeGreaterThanOrEqual(0);
      expect(r.hi).toBeLessThanOrEqual(400);
      expect(r.lo).toBeLessThanOrEqual(r.x);
      expect(r.x).toBeLessThanOrEqual(r.hi);
    }
  });
  test("an empty page yields no rows", () => {
    expect(ladderRows({ ...ladderPage, items: [] }, 100).rows).toEqual([]);
  });
});

describe("ladderFooter", () => {
  test("shows nu, iterations, convergence and counts", () => {
    const f = ladderFooter(ladderPage);
    expect(f.nu).toBe("0.62");
    expect(f.iterations).toBe(41);
    expect(f.converged).toBe(true);
    expect(f.counts).toContain("9 human");
    expect(f.warning).toBeNull();
  });
  test("slot A win rate warns only outside [0.4, 0.6] with n_ab >= 20", () => {
    const base = structuredClone(ladderPage);
    base.diagnostics = { slot_a_win_rate: 0.75, n_ab: 19 };
    expect(ladderFooter(base).warning).toBeNull();
    base.diagnostics = { slot_a_win_rate: 0.75, n_ab: 20 };
    expect(ladderFooter(base).warning).toContain("75%");
    base.diagnostics = { slot_a_win_rate: 0.4, n_ab: 40 };
    expect(ladderFooter(base).warning).toBeNull();
    base.diagnostics = { slot_a_win_rate: null, n_ab: 0 };
    expect(ladderFooter(base).warning).toBeNull();
  });
});
