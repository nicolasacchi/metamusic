import { describe, expect, test } from "bun:test";
import { canRevoke, emptyMandateForm, formToMandate, parseRoots, reasonError, scopeSummary, stateLabel, usageBar, type MandateForm } from "./mandateView";

const now = new Date("2026-11-22T10:00:00Z");

function good(): MandateForm {
  const f = emptyMandateForm();
  f.title = "First week";
  f.budget = "5";
  f.ceiling = "1";
  f.until = "2026-11-25T10:00";
  return f;
}

describe("mandate form", () => {
  test("no money field is prefilled", () => {
    const f = emptyMandateForm();
    expect(f.budget).toBe("");
    expect(f.ceiling).toBe("");
    expect(formToMandate(f, now).ok).toBe(false);
  });
  test("a valid form builds an integer micro-USD body for the night shift", () => {
    const r = formToMandate(good(), now);
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.body.holder).toBe("night_shift");
    expect(r.body.budget_micro_usd).toBe(5_000_000);
    expect(r.body.scope.max_batch_ceiling_micro_usd).toBe(1_000_000);
    expect(r.body.scope).toMatchObject({ modes: ["new_seed"], engines: ["yue2"], min_stars: 4, root_track_ids: null, max_batches_per_day: 1, tier_b: false });
    expect(r.body.valid_from).toBeUndefined();
    expect(Number.isInteger(r.body.budget_micro_usd)).toBe(true);
  });
  test("the 31-day limit is judged on the two instants, at the boundary", () => {
    const f = good();
    f.from = "2026-11-22T10:00";
    f.until = "2026-12-22T10:00"; // 30 days
    expect(formToMandate(f, now).ok).toBe(true);
    f.until = "2027-01-22T10:00"; // 61 days
    expect(formToMandate(f, now).ok).toBe(false);
  });
  test("a start time, lineages and listening checks are carried over", () => {
    const f = good();
    f.from = "2026-11-22T10:30";
    f.roots = "a1, b2\nc3";
    f.tierB = true;
    f.modes = { new_seed: true, style_edit: true };
    const r = formToMandate(f, now);
    expect(r.ok && r.body.valid_from).toBeTruthy();
    expect(r.ok && r.body.scope.root_track_ids).toEqual(["a1", "b2", "c3"]);
    expect(r.ok && r.body.scope.modes).toEqual(["new_seed", "style_edit"]);
    expect(r.ok && r.body.scope.tier_b).toBe(true);
  });
  test.each([
    ["title", (f: MandateForm) => (f.title = "  ")],
    ["budget text", (f: MandateForm) => (f.budget = "1e3")],
    ["budget decimals", (f: MandateForm) => (f.budget = "1.0000001")],
    ["budget above 100", (f: MandateForm) => (f.budget = "100.01")],
    ["ceiling above budget", (f: MandateForm) => (f.ceiling = "5.01")],
    ["ceiling empty", (f: MandateForm) => (f.ceiling = "")],
    ["no end", (f: MandateForm) => (f.until = "")],
    ["end before start", (f: MandateForm) => (f.until = "2026-11-20T10:00")],
    ["over 31 days", (f: MandateForm) => (f.until = "2027-01-01T10:00")],
    ["takes per night", (f: MandateForm) => (f.takesPerNight = "13")],
    ["takes per lineage", (f: MandateForm) => (f.takesPerLineage = "0")],
    ["stars", (f: MandateForm) => (f.minStars = "6")],
    ["no mode", (f: MandateForm) => (f.modes = { new_seed: false, style_edit: false })],
    ["duplicate lineage", (f: MandateForm) => (f.roots = "a a")],
  ])("refuses: %s", (_n, mutate) => {
    const f = good();
    mutate(f);
    const r = formToMandate(f, now);
    expect(r.ok).toBe(false);
  });
});

describe("mandate display", () => {
  test("usage bar is integer percents that never exceed 100", () => {
    expect(usageBar({ settled_micro_usd: 250_000, reserved_micro_usd: 100_000, remaining_micro_usd: 650_000 }, 1_000_000)).toEqual({ settledPct: 25, reservedPct: 10, remainingPct: 65 });
    const over = usageBar({ settled_micro_usd: 900_000, reserved_micro_usd: 900_000, remaining_micro_usd: 900_000 }, 1_000_000);
    expect(over.settledPct + over.reservedPct + over.remainingPct).toBe(100);
    expect(usageBar({ settled_micro_usd: 1, reserved_micro_usd: 1, remaining_micro_usd: 1 }, 0)).toEqual({ settledPct: 0, reservedPct: 0, remainingPct: 0 });
  });
  test("only live and scheduled mandates can be revoked", () => {
    expect(["live", "scheduled", "ended", "revoked"].map((s) => canRevoke({ effective_state: s as never }))).toEqual([true, true, false, false]);
    expect(stateLabel("live")).toBe("Live");
    expect(stateLabel("x")).toBe("x");
  });
  test("reason and lineage parsing", () => {
    expect(reasonError("  ")).toBe("A reason is required.");
    expect(reasonError("x".repeat(501))).not.toBeNull();
    expect(reasonError("done")).toBeNull();
    expect(parseRoots(" a,b  c\n")).toEqual(["a", "b", "c"]);
  });
  test("scope summary", () => {
    const s = { modes: ["new_seed"], engines: ["yue2"], min_stars: 4, root_track_ids: null, max_takes_per_lineage: 1, max_takes_per_batch: 3, max_batch_ceiling_micro_usd: 1, max_batches_per_day: 1, tier_b: false };
    expect(scopeSummary(s)).toBe("new_seed, 4+ stars, every lineage, up to 3 takes a night");
    expect(scopeSummary({ ...s, root_track_ids: ["a"], tier_b: true })).toContain("1 lineage,");
  });
});
