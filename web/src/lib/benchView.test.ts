import { describe, expect, test } from "bun:test";
import { ariaFor, band, isSlotBiased, disagreementHref, percent, ruleText, scoreRows, stateText, tierText, type Critic, type Score } from "./benchView";

const score = (o: Partial<Score> = {}): Score => ({ pairs: 0, n_eff: 0, agree: 0, predicted: 0, rate: 0, lo: 0, hi: 1, state: "insufficient", coverage: 0, ...o });
const rule = { min_n: 30, trusted_lower: 0.6, contrary_upper: 0.4 };

describe("bench view", () => {
  test("band geometry is clamped percent", () => {
    expect(band({ lo: 0.52, hi: 0.81, rate: 0.7 }, rule)).toEqual({ lo: 52, width: 29, rate: 70, contraryAt: 40, trustedAt: 60 });
    expect(band({ lo: -1, hi: 3, rate: NaN }, rule)).toMatchObject({ lo: 0, width: 100, rate: 0 });
  });
  test("labels", () => {
    expect(["trusted", "contrary", "unknown", "insufficient", "zz"].map(stateText)).toEqual(["Trusted", "Contrary", "Unknown", "Not enough pairs", "zz"]);
    expect(tierText("A")).toBe("Tier A (measured)");
    expect(percent(0.456)).toBe("46%");
    expect(ruleText(rule)).toContain("at least 30 independent pairs");
  });
  test("only scores with pairs are listed", () => {
    const c = { scores: { ratings: score({ pairs: 12 }), duels: score(), lines: score({ pairs: 3 }) } } as unknown as Critic;
    expect(scoreRows(c).map((r) => r.key)).toEqual(["ratings", "lines"]);
    // the wire sends null for a reference the critic is not compared on
    const metric = { scores: { ratings: score({ pairs: 2 }), duels: null, lines: null }, slot_bias: null } as unknown as Critic;
    expect(scoreRows(metric).map((r) => r.key)).toEqual(["ratings"]);
    expect(isSlotBiased(metric)).toBe(false);
    expect(isSlotBiased({ slot_bias: { biased: true } } as unknown as Critic)).toBe(true);
  });
  test("aria text and disagreement links", () => {
    expect(ariaFor("Your stars", score({ pairs: 10, agree: 7, rate: 0.7, lo: 0.4, hi: 0.9, state: "unknown" }))).toBe("Your stars: agrees on 7 of 10 pairs, 70% (95% interval 40% to 90%), Unknown");
    expect(disagreementHref("a|b")).toBe("/compare?a=a&b=b");
    expect(disagreementHref("duel1")).toBe("/duels/duel1");
  });
});
