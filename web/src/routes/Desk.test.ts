import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Desk from "./Desk.svelte";
import DeskPlan from "./DeskPlan.svelte";
import type { Plan, PlanSummary } from "../lib/deskApi";
import { criticVerdictOf, editRationaleOf, ownerVerdictOf } from "../lib/deskView";

const html = (c: unknown, props: Record<string, unknown>) => render(c as never, { props } as never).body;

// A plan as the server sends it for a take nobody has judged: verdicts and edit are null, not objects (schema.d.ts says object).
const serverPlan = {
  id: "p1", source: "take", job_id: null, batch_id: null, seed_index: null, track_id: "t1", parent_plan_id: null,
  base: { track_id: "t1", draft_id: null, title: "Alice's tune" }, seed: 1, cot: "full", title: "Alice's tune", bars: 2, meter: "4/4", q: "1/4=120",
  seconds_per_bar: 2, expected_audio_s: 4, truncated: null, verdict: null, critic_verdict: null, rendered: 0,
  score: { sha256: "x", bytes: 10, dialect: "yue2", aligned: true, abc_tokens: null, voices: [{ id: "1", name: null, bars: 2 }] },
  sections: [{ idx: 0, label: "verse", kind: "verse", start_bar: 0, bars: 2, start_s: 0, end_s: 4 }],
  abc: "X:1\nK:C\nCDEF|CDEF|]\n", bar_spans: [], edit: null, children: [], verdicts: { owner: null, critic: null },
} as unknown as Plan;

describe("DeskPlan", () => {
  test("renders a plan the way the server sends it (null verdicts and edit)", () => {
    const h = html(DeskPlan, { id: "p1", initial: serverPlan, mayEdit: true, mayJudge: true });
    expect(h).toContain('data-testid="plan-title"');
    expect(h).toContain("Alice's tune");
    expect(h).toContain('data-testid="bar-editor"');
    expect(h).toContain('data-testid="verdict-like"');
    expect(h).toContain('data-verdict=""');
    expect(h).toContain("bars 1–2");
    expect(h).not.toContain("plan-rationale");
  });
  test("an Editor who may edit but not judge gets the editor and no verdict buttons", () => {
    const h = html(DeskPlan, { id: "p1", initial: serverPlan, mayEdit: true, mayJudge: false });
    expect(h).toContain('data-testid="bar-editor"');
    expect(h).not.toContain("verdict-like");
  });
  test("a caller who cannot edit sees the notation and no editor", () => {
    const h = html(DeskPlan, { id: "p1", initial: serverPlan, mayEdit: false, mayJudge: false });
    expect(h).toContain('data-testid="score-view"');
    expect(h).not.toContain("bar-editor");
  });
  test("an edit names its parent and its reason; an Editor sees the Owner's verdict as text", () => {
    const edited = { ...serverPlan, parent_plan_id: "p0", edit: { ops: [], rationale: "slower" }, verdicts: { owner: { verdict: "liked", note: null, decided_at: "" }, critic: null } } as unknown as Plan;
    const h = html(DeskPlan, { id: "p2", initial: edited, mayEdit: true, mayJudge: false });
    expect(h).toContain("Why: slower");
    expect(h).toContain('href="/desk/plans/p0"');
    expect(h).toContain("Owner's verdict: Liked");
  });
  test("loading before the first answer", () => {
    expect(html(DeskPlan, { id: "p1" })).toContain('data-testid="plan-loading"');
  });
});

describe("Desk", () => {
  const item = (id: string, o: Partial<PlanSummary> = {}) =>
    ({ id, source: "take", job_id: null, batch_id: null, seed_index: null, track_id: "t1", parent_plan_id: null, base: { track_id: "t1", draft_id: null, title: "Alice's tune" }, seed: 1, cot: "full", title: "Alice's tune", bars: 6, meter: "4/4", q: null, seconds_per_bar: 2, expected_audio_s: 12, truncated: null, verdict: null, critic_verdict: null, rendered: 0, created_at: "2026-11-21T10:00:00Z", created_by: { kind: "human", id: "x", name: "alice" }, ...o }) as PlanSummary;
  test("lists plans grouped by their take with a link to each", () => {
    const h = html(Desk, { initial: [item("p1"), item("p2", { source: "edit", verdict: "liked" })] });
    expect(h).toContain('data-testid="desk-groups"');
    expect((h.match(/data-testid="desk-group"/g) ?? []).length).toBe(1);
    expect(h).toContain('href="/desk/plans/p1"');
    expect(h).toContain('data-verdict="liked"');
    expect(h).toContain('href="/tracks/t1"');
  });
  test("an empty desk says how to start; the score check is always there", () => {
    const h = html(Desk, { initial: [] });
    expect(h).toContain('data-testid="desk-empty"');
    expect(h).toContain('data-testid="desk-check"');
  });
  test("a job link scopes the list and offers to show every plan", () => {
    expect(html(Desk, { initial: [], query: { job_id: "j1" } })).toContain('data-testid="desk-clear-scope"');
    expect(html(Desk, { initial: [] })).not.toContain("desk-clear-scope");
  });
  test("loading before the first answer", () => {
    expect(html(Desk, {})).toContain('data-testid="desk-loading"');
  });
});

describe("loose reads", () => {
  test("null and missing verdicts and edit read as none", () => {
    expect(ownerVerdictOf({ verdicts: { owner: null } })).toBeNull();
    expect(ownerVerdictOf({})).toBeNull();
    expect(ownerVerdictOf({ verdicts: { owner: { verdict: "liked" } } })).toBe("liked");
    expect(criticVerdictOf({ verdicts: { critic: null }, critic_verdict: "rejected" })).toBe("rejected");
    expect(editRationaleOf({ edit: null })).toBeNull();
    expect(editRationaleOf({ edit: { rationale: "r" } })).toBe("r");
  });
});
