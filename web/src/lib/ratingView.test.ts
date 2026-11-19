import { describe, expect, test } from "bun:test";
import { averageLabel, mineOf, ratingView, starsLabel } from "./ratingView";

const by = { id: "u1", kind: "human", name: "x" };
const own = { by, stars: 4, updated_at: "2026-01-01T00:00:00Z", verdict: "keeper" };
const base = { track_id: "t1", critic: null, human: own, mine: own };

describe("ratingView shapes per role (C306.4)", () => {
  test("Owner: mine, others and average", () => {
    const v = ratingView({
      ...base,
      others: [
        { user: "bob", stars: 3, verdict: null, updated_at: "x" },
        { user: "carol", stars: null, verdict: "slow", updated_at: "x" },
      ],
      average: { mean: 3.6666, n: 3 },
    });
    expect(v.mine).toEqual({ stars: 4, verdict: "keeper" });
    expect(v.others).toEqual([
      { user: "bob", stars: 3, verdict: null },
      { user: "carol", stars: null, verdict: "slow" },
    ]);
    expect(v.average).toEqual({ mean: 3.6666, n: 3 });
    expect(averageLabel(v.average!)).toBe("3.67 from 3 ratings");
  });
  test("Editor: mine and average, no others key", () => {
    const v = ratingView({ ...base, average: { mean: 4, n: 1 } });
    expect(v.others).toBeNull();
    expect(v.average).toEqual({ mean: 4, n: 1 });
    expect(averageLabel(v.average!)).toBe("4 from 1 rating");
  });
  test("Listener: mine only; never an average, even when the key is absent or null", () => {
    const v = ratingView({ track_id: "t1", critic: null, human: own, mine: own });
    expect(v.others).toBeNull();
    expect(v.average).toBeNull();
    expect(v.mine?.stars).toBe(4);
    expect(ratingView({ ...base, others: null } as never).others).toBeNull();
  });
  test("malformed or empty optional fields are ignored, not trusted", () => {
    const v = ratingView({
      ...base,
      others: [{ user: "", stars: 2 }, { stars: 2 }, null, { user: "bob", stars: null, verdict: null }, { user: "dan", stars: 5, verdict: null }] as never,
      average: { mean: "4", n: 2 } as never,
    });
    expect(v.others).toEqual([{ user: "dan", stars: 5, verdict: null }]);
    expect(v.average).toBeNull();
    expect(ratingView({ ...base, average: { mean: 0, n: 0 } }).average).toBeNull();
    expect(ratingView({ ...base, others: "nope" } as never).others).toBeNull();
  });
  test("mine falls back to the deprecated human alias only when the key is missing", () => {
    expect(mineOf({ human: own })?.stars).toBe(4);
    expect(mineOf({ human: own, mine: null })).toBeNull();
    expect(mineOf(undefined)).toBeNull();
    expect(ratingView(null).mine).toBeNull();
  });
  test("critic stays null for a Listener", () => {
    expect(ratingView({ ...base, critic: null }).critic).toBeNull();
    expect(ratingView({ ...base, critic: { by, stars: 2, updated_at: "x", verdict: null } }).critic).toEqual({ stars: 2, verdict: null });
  });
  test("labels", () => {
    expect(starsLabel({ stars: 3, verdict: null })).toBe("★3");
    expect(starsLabel({ stars: null, verdict: "x" })).toBe("no stars");
  });
});
