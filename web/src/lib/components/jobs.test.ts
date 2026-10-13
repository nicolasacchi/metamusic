import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import JobRow from "./JobRow.svelte";
import BatchCard from "./BatchCard.svelte";
import JobTimeline from "./JobTimeline.svelte";
import SpendChart from "./SpendChart.svelte";
import { JOB_STATES, isTerminal, type JobState } from "../jobState";

const html = (c: unknown, props: Record<string, unknown>) => render(c as never, { props } as never).body;
const noop = () => {};
const job = (state: JobState) => ({
  id: "job-" + state,
  batch_id: "b1",
  state,
  title: "Take",
  take_index: 0,
  seed: 7,
  request_id: "r1",
  draft_id: null,
  parent_track_id: null,
  track_id: null,
  operator: "human",
  error_code: null,
  outcome: null,
  created_at: "2026-10-14T10:00:00Z",
  updated_at: "2026-10-14T10:00:00Z",
  cost: { basis: "estimate", expected_micro_usd: 326217, expected_usd: "0.326217", display_micro_usd: null, display_usd: null },
});

describe("JobRow", () => {
  for (const s of JOB_STATES) {
    test(`Terminate is ${isTerminal(s) ? "absent" : "rendered"} for ${s}`, () => {
      const h = html(JobRow, { job: job(s), viewer: { kind: "human" }, onterminate: noop });
      if (isTerminal(s)) expect(h).not.toContain('data-testid="terminate-job"');
      else expect(h).toContain('data-testid="terminate-job"');
    });
  }

  test("renders cost from integer micro-USD with its basis and links the job", () => {
    const h = html(JobRow, { job: job("generating"), viewer: { kind: "human" }, onterminate: noop });
    expect(h).toContain("$0.33");
    expect(h).toContain("estimate");
    expect(h).toContain('href="/jobs/job-generating"');
  });

  test("a reconciled display amount wins over the estimate", () => {
    const j = { ...job("completed"), cost: { basis: "reconciled", expected_micro_usd: 326217, expected_usd: "x", display_micro_usd: 5800, display_usd: "x" } };
    const h = html(JobRow, { job: j, viewer: { kind: "human" }, onterminate: noop });
    expect(h).toContain("$0.0058");
  });
});

describe("BatchCard", () => {
  const batch = (state: string) => ({
    id: "b1",
    state,
    takes: 3,
    created_at: "2026-10-14T10:00:00Z",
    created_by: { id: "human", kind: "human", name: "human" },
    approval: { state: "approved" },
    counts: { awaiting: 0, queued: 1, running: 1, completed: 1, truncated: 0, failed: 0, cancelled: 0, expired: 0 },
    cost: { settled_micro_usd: 5800, reserved_micro_usd: 0, outstanding_micro_usd: 100000, expected_micro_usd: 326217, max_micro_usd: 691731, ceiling_micro_usd: 691731, reconciled: false },
  });
  test("Terminate batch only while the batch is not final", () => {
    expect(html(BatchCard, { batch: batch("active"), viewer: { kind: "human" }, onterminate: noop })).toContain('data-testid="terminate-batch"');
    expect(html(BatchCard, { batch: batch("finished"), viewer: { kind: "human" }, onterminate: noop })).not.toContain('data-testid="terminate-batch"');
  });
  test("costs come from micro-USD", () => {
    const h = html(BatchCard, { batch: batch("active"), viewer: { kind: "human" }, onterminate: noop });
    expect(h).toContain("$0.0058");
    expect(h).toContain("$0.33");
  });
});

describe("JobTimeline", () => {
  test("lists events", () => {
    const h = html(JobTimeline, { events: [{ at: "2026-10-14T10:00:00Z", event: "state", from: "queued", to: "starting", code: null }] });
    expect(h).toContain("queued");
    expect(h).toContain("starting");
  });
  test("empty", () => {
    expect(html(JobTimeline, { events: [] })).toContain("No events");
  });
});

describe("SpendChart", () => {
  test("one bar per day, amounts as dollars from micro-USD", () => {
    const h = html(SpendChart, { days: [{ date: "2026-10-01", settled_micro_usd: 1000000 }, { date: "2026-10-02", settled_micro_usd: 500000 }] });
    expect((h.match(/data-testid="spend-bar"/g) ?? []).length).toBe(2);
    expect(h).toContain("$1.00");
    expect(h).toContain("$0.50");
  });
  test("no days", () => {
    expect(html(SpendChart, { days: [] })).toContain("No spend");
  });
});
