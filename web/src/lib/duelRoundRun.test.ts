import { describe, expect, test } from "bun:test";
import { RateLimited } from "../api/client";
import { runBlindRound, type CreateDuel, type RoundProgress } from "./duelRoundRun";

const pairs: [string, string][] = [
  ["a", "b"],
  ["a", "c"],
  ["b", "c"],
];
const noSleep = () => Promise.resolve();

describe("runBlindRound", () => {
  test("one key per pair, navigates to the first created id", async () => {
    const keys: string[] = [];
    const create: CreateDuel = (p, key) => {
      keys.push(key);
      return Promise.resolve({ id: `duel-${p[0]}${p[1]}` });
    };
    const out: string[] = [];
    const r = await runBlindRound(pairs, { create, sleep: noSleep, navigate: (to) => out.push(to) });
    expect(r.created).toEqual(["duel-ab", "duel-ac", "duel-bc"]);
    expect(new Set(keys).size).toBe(3);
    expect(out).toEqual(["/duels/duel-ab"]);
  });

  test("the key is reused across a RateLimited error and waits retryAfterS", async () => {
    const seen: string[] = [];
    const slept: number[] = [];
    const progress: RoundProgress[] = [];
    let calls = 0;
    const create: CreateDuel = (_p, key) => {
      seen.push(key);
      if (++calls === 1) return Promise.reject(new RateLimited(7));
      return Promise.resolve({ id: "d" + calls });
    };
    await runBlindRound([pairs[0]!], {
      create,
      sleep: (ms) => {
        slept.push(ms);
        return Promise.resolve();
      },
      navigate: () => {},
      onProgress: (p) => progress.push(p),
    });
    expect(seen).toHaveLength(2);
    expect(seen[0]).toBe(seen[1]!);
    expect(slept).toEqual([7000]);
    expect(progress.some((p) => p.waitingS === 7)).toBe(true);
  });

  test("the key is reused across a network error", async () => {
    const seen: string[] = [];
    let calls = 0;
    const create: CreateDuel = (_p, key) => {
      seen.push(key);
      if (++calls < 3) return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve({ id: "ok" });
    };
    const r = await runBlindRound([pairs[0]!], { create, sleep: noSleep, navigate: () => {} });
    expect(new Set(seen).size).toBe(1);
    expect(seen).toHaveLength(3);
    expect(r.created).toEqual(["ok"]);
  });

  test("a next pair gets a fresh key", async () => {
    const seen: string[] = [];
    const create: CreateDuel = (_p, key) => {
      seen.push(key);
      return Promise.resolve({ id: key });
    };
    await runBlindRound(pairs.slice(0, 2), { create, sleep: noSleep, navigate: () => {} });
    expect(seen[0]).not.toBe(seen[1]!);
  });

  test("progress counts done pairs", async () => {
    const progress: RoundProgress[] = [];
    await runBlindRound(pairs, {
      create: (_p, k) => Promise.resolve({ id: k }),
      sleep: noSleep,
      navigate: () => {},
      onProgress: (p) => progress.push({ ...p }),
    });
    expect(progress.at(-1)).toMatchObject({ done: 3, total: 3 });
  });

  test("a non-retryable error stops the round and reports what was created", async () => {
    let n = 0;
    const create: CreateDuel = () => {
      if (++n === 2) return Promise.reject(new Error("TRACK_NOT_FOUND"));
      return Promise.resolve({ id: "d" + n });
    };
    const out: string[] = [];
    const r = await runBlindRound(pairs, { create, sleep: noSleep, navigate: (t) => out.push(t) });
    expect(r.created).toEqual(["d1"]);
    expect(r.error).toBe("TRACK_NOT_FOUND");
    expect(out).toEqual(["/duels/d1"]);
  });

  test("gives up after the retry budget", async () => {
    const create: CreateDuel = () => Promise.reject(new TypeError("offline"));
    const r = await runBlindRound([pairs[0]!], { create, sleep: noSleep, navigate: () => {}, maxAttempts: 3 });
    expect(r.created).toEqual([]);
    expect(r.error).toBeTruthy();
  });

  test("no pairs: nothing happens", async () => {
    const out: string[] = [];
    const r = await runBlindRound([], { create: () => Promise.reject(new Error("no")), sleep: noSleep, navigate: (t) => out.push(t) });
    expect(r.created).toEqual([]);
    expect(out).toEqual([]);
  });
});
