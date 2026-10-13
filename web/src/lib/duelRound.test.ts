import { describe, expect, test } from "bun:test";
import { BLIND_ROUND_MAX_PAIRS, blindRoundPairs, canBlindRound } from "./duelRound";

/** Deterministic xorshift RNG in [0, 1). */
function rng(seed: number): () => number {
  let s = seed >>> 0 || 1;
  return () => {
    s ^= s << 13;
    s >>>= 0;
    s ^= s >>> 17;
    s ^= s << 5;
    s >>>= 0;
    return s / 0x100000000;
  };
}
const ids = (n: number) => Array.from({ length: n }, (_, i) => `t${i}`);
const key = (p: readonly [string, string]) => [...p].sort().join("|");

describe("blindRoundPairs", () => {
  test("2, 3 and 6 tracks give every pair: 1, 3 and 15", () => {
    expect(blindRoundPairs(ids(2), rng(1))).toHaveLength(1);
    expect(blindRoundPairs(ids(3), rng(1))).toHaveLength(3);
    expect(blindRoundPairs(ids(6), rng(1))).toHaveLength(15);
  });
  test("all pairs are distinct and cover the full set", () => {
    const got = new Set(blindRoundPairs(ids(6), rng(7)).map(key));
    expect(got.size).toBe(15);
  });
  test("8 tracks give 15 random distinct pairs of known tracks", () => {
    const pairs = blindRoundPairs(ids(8), rng(3));
    expect(pairs).toHaveLength(BLIND_ROUND_MAX_PAIRS);
    expect(new Set(pairs.map(key)).size).toBe(15);
    for (const [a, b] of pairs) {
      expect(a).not.toBe(b);
      expect(ids(8)).toContain(a);
      expect(ids(8)).toContain(b);
    }
  });
  test("order is randomised by the injected RNG and reproducible", () => {
    const a = blindRoundPairs(ids(6), rng(1)).map(key);
    const b = blindRoundPairs(ids(6), rng(1)).map(key);
    const c = blindRoundPairs(ids(6), rng(99)).map(key);
    expect(a).toEqual(b);
    expect(a).not.toEqual(c);
    expect(a).not.toEqual(blindRoundPairs(ids(6), () => 0.999999).map(key).sort());
  });
  test("duplicate ids are ignored", () => {
    expect(blindRoundPairs(["x", "x", "y"], rng(1))).toHaveLength(1);
  });
  test("1 track or none: no pairs and the button is disabled", () => {
    expect(blindRoundPairs(ids(1), rng(1))).toEqual([]);
    expect(blindRoundPairs([], rng(1))).toEqual([]);
    expect(canBlindRound(1)).toBe(false);
    expect(canBlindRound(0)).toBe(false);
    expect(canBlindRound(2)).toBe(true);
  });
});
