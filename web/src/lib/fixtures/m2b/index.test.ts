import { describe, expect, test } from "bun:test";
import { comparison, dossier, duelOpenBlind, duelRevealed, ladderPage, spectrogramView } from "./index";

describe("M2b fixtures (TestWebFixtures_M2b)", () => {
  test("the open blind duel hides tracks, gains and loudness", () => {
    expect(duelOpenBlind.state).toBe("open");
    expect(duelOpenBlind.blind).toBe(true);
    expect(duelOpenBlind.revealed).toBe(false);
    for (const slot of [duelOpenBlind.slots.a, duelOpenBlind.slots.b]) {
      expect(slot.track).toBeNull();
      expect(slot.gain_db).toBeNull();
      expect(slot.window_lufs).toBeNull();
      expect(slot.clip.url.startsWith("/media/")).toBe(true);
    }
    expect(duelOpenBlind.my_verdict).toBeNull();
    expect(duelOpenBlind.human_verdict).toBeNull();
  });

  test("the revealed duel names both tracks and the verdicts", () => {
    expect(duelRevealed.revealed).toBe(true);
    expect(duelRevealed.slots.a.track?.title).toBe("Synthetic Beta");
    expect(duelRevealed.human_verdict?.verdict).toBe("a");
    expect(duelRevealed.critic_verdicts[0]?.source).toBe("critic");
  });

  test("the ladder is ranked and anchored at the reject item", () => {
    expect(ladderPage.model.anchor_u).toBe(0);
    const us = ladderPage.items.map((i) => i.u);
    expect([...us].sort((a, b) => b - a)).toEqual(us);
    expect(ladderPage.items.map((i) => i.rank)).toEqual([1, 2, 3]);
    expect(ladderPage.items[2]?.provisional).toBe(true);
  });

  test("the dossier leads with flags and always carries blind spots", () => {
    expect(dossier.tier).toBe("A");
    expect(dossier.flags.map((f) => f.code)).toContain("clipping");
    expect(dossier.blind_spots.length).toBeGreaterThanOrEqual(6);
    expect(dossier.token_loops?.streams[0]?.periodic_run.period_tokens).toBe(25);
  });

  test("the spectrogram view maps a click to a time through its plot", () => {
    expect(spectrogramView.content_type).toBe("image/jpeg");
    expect(spectrogramView.bytes).toBeLessThan(100_000);
    expect(spectrogramView.plot.x0).toBe(142);
    expect(spectrogramView.image).toBeUndefined();
  });

  test("the comparison carries b-minus-a deltas and a relation", () => {
    expect(comparison.relation).toBe("siblings");
    expect(comparison.deltas.integrated_lufs).toBe(-2.1);
    expect(comparison.ladder.a?.n_duels).toBe(6);
  });
});
