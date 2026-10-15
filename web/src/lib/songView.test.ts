import { describe, expect, test } from "bun:test";
import {
  MAX_PENDING_REFETCH,
  PENDING_REFETCH_MS,
  acceptProposalRequest,
  dismissProposalRequest,
  eventText,
  orderTakes,
  pendingDelay,
  pinAction,
  selectForDuel,
  tempoText,
  type TakeLike,
} from "./songView";

const take = (id: string, recorded_at: string, over: Partial<TakeLike> = {}): TakeLike => ({ id, recorded_at, is_pick: false, pinned: false, status: "draft", ...over });

describe("orderTakes", () => {
  test("pick first, then recorded_at descending as instants, then id descending", () => {
    const takes = [
      take("a", "2026-01-01T10:00:00Z"),
      take("b", "2026-01-01T10:00:00.250Z"),
      take("c", "2026-01-01T10:00:00.250Z"),
      take("p", "2025-01-01T00:00:00.000Z", { is_pick: true }),
      take("d", "2026-02-01T00:00:00.000Z"),
    ];
    expect(orderTakes(takes).map((t) => t.id)).toEqual(["p", "d", "c", "b", "a"]);
  });
  test("does not mutate its input and mixes 20- and 24-character timestamps correctly", () => {
    const takes = [take("x", "2026-01-01T10:00:00Z"), take("y", "2026-01-01T10:00:00.000Z")];
    const copy = [...takes];
    expect(orderTakes(takes).map((t) => t.id)).toEqual(["y", "x"]);
    expect(takes).toEqual(copy);
  });
});

describe("selectForDuel", () => {
  test("toggles on and off", () => {
    expect(selectForDuel([], "a")).toEqual(["a"]);
    expect(selectForDuel(["a"], "b")).toEqual(["a", "b"]);
    expect(selectForDuel(["a", "b"], "a")).toEqual(["b"]);
  });
  test("exactly two: a third replaces the oldest", () => {
    expect(selectForDuel(["a", "b"], "c")).toEqual(["b", "c"]);
    expect(selectForDuel(selectForDuel(["a", "b"], "c"), "d")).toEqual(["c", "d"]);
  });
});

describe("pendingDelay", () => {
  test("1.5 s, at most three refetches, only while pending", () => {
    expect(PENDING_REFETCH_MS).toBe(1500);
    expect(MAX_PENDING_REFETCH).toBe(3);
    expect(pendingDelay(true, 0)).toBe(1500);
    expect(pendingDelay(true, 2)).toBe(1500);
    expect(pendingDelay(true, 3)).toBeNull();
    expect(pendingDelay(false, 0)).toBeNull();
  });
});

describe("pinAction", () => {
  test("pin for an eligible take, unpin for the pinned one, nothing for a discarded take", () => {
    expect(pinAction(take("a", "2026-01-01T00:00:00Z"))).toBe("pin");
    expect(pinAction(take("a", "2026-01-01T00:00:00Z", { pinned: true }))).toBe("unpin");
    expect(pinAction(take("a", "2026-01-01T00:00:00Z", { status: "discarded" }))).toBe("none");
  });
  test("a pinned take that is discarded can still be unpinned", () => {
    expect(pinAction(take("a", "2026-01-01T00:00:00Z", { status: "discarded", pinned: true }))).toBe("unpin");
  });
});

describe("proposal actions", () => {
  const p = { id: "pp1", song_id: "s1", take_id: "t9" };
  test("accept maps to pin_pick with the proposal id", () => {
    expect(acceptProposalRequest(p)).toEqual({ op: "pin_pick", songId: "s1", body: { take_id: "t9", proposal_id: "pp1" } });
  });
  test("dismiss maps to dismiss_pick_proposal", () => {
    expect(dismissProposalRequest(p)).toEqual({ op: "dismiss_pick_proposal", proposalId: "pp1" });
  });
});

describe("tempoText", () => {
  const t = (requested_bpm: number | null, measured_bpm: number | null, mismatch = false, octave = false) => ({ requested_bpm, measured_bpm, mismatch, octave });
  test.each([
    ["agree", t(76, 75), "requested 76 · measured 75 ✓"],
    ["mismatch", t(76, 120, true), "requested 76 · measured 120 ⚠"],
    ["octave", t(76, 152, true, true), "requested 76 · measured 152 ≈"],
    ["requested only", t(76, null), "requested 76 · estimate"],
    ["measured only", t(null, 75), "measured 75"],
    ["none", t(null, null), ""],
  ])("%s", (_n, input, want) => {
    expect(tempoText(input)).toBe(want);
  });
  test("null yields an empty string", () => {
    expect(tempoText(null)).toBe("");
  });
});

describe("eventText", () => {
  test("renders the reason in plain words", () => {
    expect(eventText({ reason: "pin", to_source: "pinned" })).toBe("pinned");
    expect(eventText({ reason: "unpin", to_source: "stars" })).toBe("unpinned, now by stars");
    expect(eventText({ reason: "pin_ineligible", to_source: "newest" })).toBe("pin not eligible, now newest");
    expect(eventText({ reason: "initial", to_source: "newest" })).toBe("first Pick (newest)");
    expect(eventText({ reason: "emptied", to_source: "none" })).toBe("no take left");
    expect(eventText({ reason: "ladder", to_source: "ladder" })).toBe("by duels");
  });
});
