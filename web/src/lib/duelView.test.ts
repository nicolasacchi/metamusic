import { describe, expect, test } from "bun:test";
import { duelOpenBlind, duelRevealed } from "./fixtures/m2b";
import { duelPage, duelSummaryRow, duelViewModel } from "./duelView";
import type { components } from "../api/schema";

describe("duelViewModel", () => {
  test("an open blind duel is playable and leaks no title, seed or gain", () => {
    const v = duelViewModel(duelOpenBlind);
    expect(v.kind).toBe("play");
    const json = JSON.stringify(v);
    for (const s of ["title", "seed", "gain", "lufs", "Synthetic"]) expect(json.toLowerCase()).not.toContain(s.toLowerCase());
    if (v.kind !== "play") throw new Error("kind");
    expect(v.slots.map((s) => s.label)).toEqual(["A", "B"]);
    expect(v.blind).toBe(true);
    expect(v.loudnessMatched).toBe(true);
  });

  test("a blind duel with tracks present (should never happen) still hides them while open", () => {
    const leaky = structuredClone(duelOpenBlind);
    leaky.slots.a.track = { id: "x", title: "Secret Title", seed: 4242, request_id: "r", root_track_id: "x", duration_s: 1, recipe_known: true, lossy: false };
    leaky.slots.a.gain_db = -3.5;
    const json = JSON.stringify(duelViewModel(leaky));
    expect(json).not.toContain("Secret Title");
    expect(json).not.toContain("4242");
    expect(json).not.toContain("-3.5");
  });

  test("a decided duel reveals titles, winner and gains", () => {
    const v = duelViewModel(duelRevealed);
    expect(v.kind).toBe("reveal");
    if (v.kind !== "reveal") throw new Error("kind");
    expect(v.outcome).toBe("A is better");
    expect(v.rows.map((r) => r.title)).toEqual([duelRevealed.slots.a.track?.title ?? null, duelRevealed.slots.b.track?.title ?? null]);
    expect(v.rows[0]!.won).toBe(true);
    expect(v.rows[1]!.won).toBe(false);
    expect(v.rows[0]!.gain).toMatch(/dB$/);
    expect(v.critics.length).toBe(duelRevealed.critic_verdicts.length);
    expect(v.compareHref).toContain("/compare?a=");
  });

  test("an expired duel yields a notice", () => {
    const e: components["schemas"]["Duel"] = { ...structuredClone(duelOpenBlind), state: "expired" };
    const v = duelViewModel(e);
    expect(v.kind).toBe("expired");
    expect(JSON.stringify(v)).not.toContain("/media/");
  });

  test("an open non-blind duel shows titles but not gains", () => {
    const d = structuredClone(duelRevealed);
    d.state = "open";
    d.human_verdict = null;
    d.my_verdict = null;
    d.blind = false;
    const v = duelViewModel(d);
    expect(v.kind).toBe("play");
    if (v.kind !== "play") throw new Error("kind");
    expect(v.slots[0]!.title).toBe(duelRevealed.slots.a.track?.title ?? "");
    expect(JSON.stringify(v)).not.toContain("gain");
  });

  test("a decided blind duel is only revealed once the server says so", () => {
    const d = structuredClone(duelRevealed);
    d.revealed = false;
    d.slots.a.track = null;
    d.slots.b.track = null;
    const v = duelViewModel(d);
    expect(v.kind).toBe("reveal");
    if (v.kind !== "reveal") throw new Error("kind");
    expect(v.rows.every((r) => r.title === null)).toBe(true);
  });
});

describe("history rows", () => {
  const summary: components["schemas"]["DuelSummary"] = {
    id: "d1",
    state: "decided",
    blind: true,
    revealed: true,
    created_at: "2026-10-13T11:00:00Z",
    expires_at: "2026-11-12T11:00:00Z",
    loudness_matched: true,
    critic_verdicts: 1,
    my_verdict: "a",
    human_verdict: { verdict: "tie", winner_track_id: null },
    window: { from_s: 0, to_s: 30, duration_s: 30 },
    slots: {
      a: { track: { id: "a", title: "One", seed: 1, request_id: "r", root_track_id: "a", duration_s: 60, recipe_known: true, lossy: false } },
      b: { track: { id: "b", title: "Two", seed: 2, request_id: "r", root_track_id: "a", duration_s: 60, recipe_known: true, lossy: false } },
    },
  };
  test("a revealed summary shows titles and links to /duels/:id", () => {
    const r = duelSummaryRow(summary);
    expect(r.href).toBe("/duels/d1");
    expect(r.title).toBe("One vs Two");
    expect(r.outcome).toBe("Tie");
  });
  test("an unrevealed blind summary hides titles", () => {
    const s = structuredClone(summary);
    s.revealed = false;
    s.state = "open";
    s.human_verdict = null;
    s.my_verdict = null;
    s.slots.a.track = null;
    s.slots.b.track = null;
    const r = duelSummaryRow(s);
    expect(r.title).toBe("Blind duel");
    expect(r.outcome).toBe("Open");
  });
  test("duelPage reports total and cursor", () => {
    const p = duelPage({ items: [summary], total: 4, next_cursor: "c" });
    expect(p).toMatchObject({ total: 4, nextCursor: "c" });
    expect(p.rows).toHaveLength(1);
  });
});
