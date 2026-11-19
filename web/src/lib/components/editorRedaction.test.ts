import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import BatchCard from "./BatchCard.svelte";
import JobRow from "./JobRow.svelte";

// U1-E1: for an Editor the server omits cost and created_by on another user's batch and job. The components must
// render without them (no throw, no money text); with them present the Owner's text is unchanged.

const counts = { completed: 1, running: 0, queued: 0, awaiting: 0, failed: 0, cancelled: 0, expired: 0, truncated: 0 };
const batchBase = { id: "bat-1", state: "active", takes: 2, counts, approval: { state: "approved" } };
const jobBase = { id: "job-1", batch_id: "bat-1", state: "queued", title: "A take", track_id: null, error_code: null };
const viewer = { kind: "human" };

// Strip Svelte's hydration comments and scoped class names so the assertions read as the visible markup.
const clean = (h: string) => h.replace(/<!--.*?-->/g, "").replace(/ class="[^"]*"/g, "");
const batchHtml = (b: object) => clean(render(BatchCard as never, { props: { batch: b, viewer, onterminate: () => {} } } as never).body);
const jobHtml = (j: object) => clean(render(JobRow as never, { props: { job: j, viewer, onterminate: () => {} } } as never).body);

describe("BatchCard without cost or created_by (Editor viewing another user's batch)", () => {
  test("renders without throwing and shows no money or creator", () => {
    const h = batchHtml(batchBase);
    expect(h).toContain('data-testid="batch-card"');
    expect(h).not.toContain('data-testid="batch-cost"');
    expect(h).not.toMatch(/\$\d/);
    expect(h).not.toContain(" by ");
  });
  test("Owner shape: cost line and creator are unchanged", () => {
    const h = batchHtml({
      ...batchBase,
      cost: { settled_micro_usd: 1_000_000, outstanding_micro_usd: 0, expected_micro_usd: 2_000_000, ceiling_micro_usd: 3_000_000 },
      created_by: { id: "u1", kind: "human", name: "Ada" },
    });
    expect(h).toContain('data-testid="batch-cost"');
    expect(h).toContain("Settled $1.00");
    expect(h).toContain("up to $3.00");
    expect(h).toContain("Approval: approved · by Ada");
  });
});

describe("JobRow without cost (Editor viewing another user's job)", () => {
  test("renders without throwing and shows a dash, no money", () => {
    const h = jobHtml(jobBase);
    expect(h).toContain('data-testid="job-row"');
    expect(h).toMatch(/data-testid="job-cost">—<\/td>/);
    expect(h).not.toMatch(/\$\d/);
  });
  test("Owner shape: cost cell is unchanged", () => {
    const h = jobHtml({ ...jobBase, cost: { basis: "estimate", expected_micro_usd: 500_000, display_micro_usd: null } });
    expect(h).toMatch(/data-testid="job-cost">\$0\.50 <span>estimate<\/span>/);
  });
});
