import { describe, expect, test } from "bun:test";
import type { PlanSummary } from "./deskApi";
import { barChangeLabel, reportLines, barText, deskPlanHref, filtersFromQuery, filtersToQuery, groupByBase, noFilters, openRefusalText, planFacts, planLabel, sectionOf, spanText, verdictText, voiceIds } from "./deskView";

const plan = (id: string, o: Partial<PlanSummary> = {}): PlanSummary =>
  ({
    id,
    bars: 8,
    base: { track_id: "t1", draft_id: null, title: "Alice's tune" },
    batch_id: null,
    cot: "full",
    created_at: "2026-11-21T10:00:00Z",
    created_by: { kind: "human", name: "alice" },
    critic_verdict: null,
    expected_audio_s: 16,
    job_id: null,
    meter: "4/4",
    parent_plan_id: null,
    q: null,
    rendered: 0,
    seconds_per_bar: 2,
    seed: 7,
    seed_index: null,
    source: "take",
    title: "Alice's tune",
    track_id: "t1",
    truncated: null,
    verdict: null,
    ...o,
  }) as PlanSummary;

describe("groupByBase", () => {
  test("groups by take, newest group first, the take's own score then seeds then edits", () => {
    const items = [
      plan("e1", { source: "edit", created_at: "2026-11-21T12:00:00Z", parent_plan_id: "p" }),
      plan("s2", { source: "job", seed_index: 1, job_id: "j1", created_at: "2026-11-21T11:00:00Z" }),
      plan("p", { source: "take" }),
      plan("s1", { source: "job", seed_index: 0, job_id: "j1", created_at: "2026-11-21T11:00:00Z" }),
      plan("o1", { base: { track_id: "t2", draft_id: null, title: "Bob's tune" }, created_at: "2026-11-22T09:00:00Z" }),
    ];
    const g = groupByBase(items);
    expect(g.map((x) => x.title)).toEqual(["Bob's tune", "Alice's tune"]);
    expect(g[1]!.plans.map((p) => p.id)).toEqual(["p", "s1", "s2", "e1"]);
    expect(g[1]!.trackId).toBe("t1");
  });
  test("a plan job with no take groups by its job", () => {
    const g = groupByBase([plan("a", { base: { track_id: null, draft_id: null, title: "Seeds" }, job_id: "j9", source: "job" }), plan("b", { base: { track_id: null, draft_id: null, title: "Seeds" }, job_id: "j9", source: "job" })]);
    expect(g).toHaveLength(1);
    expect(g[0]!.trackId).toBeNull();
  });
  test("nothing groups to nothing", () => expect(groupByBase([])).toEqual([]));
});

describe("filters", () => {
  test("the query string maps to filters, ignoring unknown values", () => {
    expect(filtersFromQuery({ job_id: "j1", source: "job", verdict: "liked", rendered: "false" })).toEqual({ source: "job", verdict: "liked", rendered: "no", jobId: "j1", batchId: "", baseTrackId: "" });
    expect(filtersFromQuery({ source: "nope", verdict: "x" })).toEqual(noFilters);
  });
  test("filters become the list_plans query; blank ones are left out", () => {
    expect(filtersToQuery(noFilters)).toEqual({ limit: 100 });
    expect(filtersToQuery({ ...noFilters, rendered: "yes", verdict: "none", baseTrackId: "t1" }, "c2")).toEqual({ limit: 100, rendered: true, verdict: "none", base_track_id: "t1", cursor: "c2" });
  });
});

describe("words", () => {
  test("labels and facts", () => {
    expect(planLabel({ source: "take", seed_index: null, seed: 1 })).toBe("Take's own score");
    expect(planLabel({ source: "job", seed_index: 2, seed: 99 })).toBe("Seed 3");
    expect(planLabel({ source: "edit", seed_index: null, seed: 1 })).toBe("Edited");
    expect(verdictText("liked")).toBe("Liked");
    expect(verdictText(null)).toBe("No verdict");
    expect(planFacts({ bars: 1, meter: "3/4", expected_audio_s: 0, truncated: null })).toBe("1 bar, 3/4");
    expect(planFacts({ bars: 8, meter: "4/4", expected_audio_s: 15.6, truncated: true })).toBe("8 bars, 4/4, about 16 s, cut short");
    expect(deskPlanHref("a/b")).toBe("/desk/plans/a%2Fb");
  });
  test("why a take cannot be opened", () => {
    const f = (reason: string) => ({ ok: false as const, status: 422, code: "PLAN_SOURCE_UNSUPPORTED", message: "m", details: { reason } });
    expect(openRefusalText(f("no_score"))).toContain("no score");
    expect(openRefusalText(f("engine"))).toContain("YuE2");
    expect(openRefusalText(f("zzz"))).toContain("no score the Desk can open");
    expect(openRefusalText({ ok: false, status: 403, code: "FORBIDDEN", message: "x", details: {} })).toContain("cannot open");
    expect(openRefusalText({ ok: false, status: 500, code: "X", message: "boom", details: {} })).toBe("boom");
  });
});

describe("bar text", () => {
  const abc = "X:1\nT:Café\nK:C\nCDEF|G4|]\n";
  const start = new TextEncoder().encode("X:1\nT:Café\nK:C\n").length;
  const spans = [
    { voice: "1", bar: 0, start, end: start + 4, start_s: 0 },
    { voice: "1", bar: 1, start: start + 5, end: start + 7, start_s: 2 },
  ];
  test("slices by bytes, so an accent before the bar does not shift it", () => {
    expect(spanText(abc, spans[0]!)).toBe("CDEF");
    expect(barText(abc, spans, "1", 1)).toBe("G4");
    expect(barText(abc, spans, "2", 0)).toBeNull();
    expect(barText(abc, null, "1", 0)).toBeNull();
  });
  test("voices and sections", () => {
    expect(voiceIds({ score: { voices: [{ id: "1", bars: 2, name: null }, { id: "2", bars: 2, name: null }] } } as never)).toEqual(["1", "2"]);
    expect(voiceIds({ score: { voices: null } } as never)).toEqual([]);
    const p = { sections: [{ idx: 0, kind: "verse", label: "", start_bar: 0, bars: 4 }, { idx: 1, kind: "chorus", label: "Hook", start_bar: 4, bars: 4 }] } as never;
    expect(sectionOf(p, 3)).toEqual({ label: "verse", idx: 0 });
    expect(sectionOf(p, 4)).toEqual({ label: "Hook", idx: 1 });
    expect(sectionOf(p, 8)).toBeNull();
    expect(sectionOf({ sections: null } as never, 0)).toBeNull();
  });
});

describe("reportLines", () => {
  const base = { ok: true, bars: 2, voices: [{ id: "1", bars: 2, name: "" }], header: { bpm: 100, key: "C", meter: "4/4", q: "1/4=100", unit: "1/4" }, duration_s: 4.4, errors: null, warnings: null };
  test("a score that reads is described by its shape", () => {
    expect(reportLines(base)).toEqual({ ok: true, lines: ["Reads as ABC: 2 bars, 1 voice, 4/4, about 4 s."] });
    expect(reportLines({ ...base, warnings: [{ code: "odd_thing", line: 3 }] }).lines[1]).toBe("Warning on line 3: odd thing");
  });
  test("one that does not read lists each error with its place", () => {
    const r = reportLines({ ...base, ok: false, errors: [{ line: 1, col: 2, rule: "empty_score" }, { line: 4, col: 1, rule: "weird_rule" }] });
    expect(r).toEqual({ ok: false, lines: ["Line 1, column 2: the score has no notes", "Line 4, column 1: weird rule"] });
    expect(reportLines({ ...base, ok: false }).lines).toEqual(["The score does not read as ABC."]);
  });
});

describe("barChangeLabel", () => {
  test("1-based, inclusive end, one bar or a range", () => {
    expect(barChangeLabel({ voice: "1", bar_from: 2, bar_to: 2 })).toBe("voice 1, bar 3");
    expect(barChangeLabel({ voice: "V2", bar_from: 0, bar_to: 3 })).toBe("voice V2, bars 1–4");
  });
});
