import { describe, expect, test } from "bun:test";
import { pickEvidenceText } from "./pickView";

describe("pickEvidenceText", () => {
  test.each([
    [{ def: "mm.pick/1", rule: "ladder", eligible: 2, duels_n: 3 }, "ahead after 3 blind duels"],
    [{ def: "mm.pick/1", rule: "ladder", eligible: 2, duels_n: 1 }, "ahead after 1 blind duel"],
    [{ def: "mm.pick/1", rule: "stars", eligible: 3, stars: 4, tied: 2 }, "4★ (newest of 2 tied)"],
    [{ def: "mm.pick/1", rule: "stars", eligible: 3, stars: 5, tied: 1 }, "5★"],
    [{ def: "mm.pick/1", rule: "newest", eligible: 2 }, "newest take"],
    [{ def: "mm.pick/1", rule: "pinned", eligible: 2, pinned_by: "u" }, "pinned by you"],
    [{ def: "mm.pick/1", rule: "none", eligible: 0 }, "no take"],
  ])("%j => %s", (ev, want) => {
    expect(pickEvidenceText(ev)).toBe(want);
  });
  test("unknown or malformed evidence degrades to an empty string", () => {
    expect(pickEvidenceText(null)).toBe("");
    expect(pickEvidenceText({})).toBe("");
    expect(pickEvidenceText({ rule: "future" })).toBe("");
    expect(pickEvidenceText({ rule: "ladder" })).toBe("ahead after blind duels");
  });
  test("a kept but ineligible pin is noted", () => {
    const t = pickEvidenceText({ rule: "newest", pin_ineligible: { take_id: "t", status: "discarded" } });
    expect(t).toBe("newest take · pin not eligible (discarded)");
  });
});
