import { expect, test, type APIRequestContext } from "./fixtures";
import { agentCreateHeaders, allTerminal, assertOnlyDuplicateFailures, composeRoots, first, getBatch, pollBatch, type Batch, type Estimate } from "./m3Api";

// Desktop Chrome only. The agent is the setup's read+write+generate PAT; its auto-approve limit is one take, so every
// agent batch of two or more takes waits for the operator. The batches are new-seed variants of the spec's own
// composed tracks (never the drafts of m3-batch).

test.describe.configure({ mode: "serial", timeout: 150_000 });

async function agentBatch(agent: APIRequestContext, mother: string, takes: number): Promise<Batch> {
  const er = await agent.post(`/api/tracks/${mother}/variants/estimate`, { data: { mode: "new_seed", n_seeds: takes } });
  expect(er.ok(), await er.text()).toBe(true);
  const e = (await er.json()) as Estimate;
  expect(e.requires_human_approval).toBe(true);
  const cr = await agent.post("/api/generations", { data: { confirmation_token: e.confirmation_token, rationale: "e2e agent batch" }, headers: agentCreateHeaders() });
  expect(cr.status(), await cr.text()).toBe(201);
  return (await cr.json()) as Batch;
}

test("an agent batch of seven takes waits for the operator, who approves it from the confirm page", async ({ page, request, playwright, baseURL, agentToken }) => {
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${agentToken}` } });
  try {
    const mother = first(await composeRoots(request, 1, "m3a"));
    const batch = await agentBatch(agent, mother, 7);
    expect(batch.state).toBe("awaiting_confirmation");
    expect(batch.approval.reasons).toContain("takes_over_limit");

    await page.goto(`/human/confirm/${batch.id}`);
    await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
    await expect(page.getByTestId("requested-by")).toHaveText("e2e-agent");
    await expect(page.getByTestId("batch-takes").locator("tbody tr")).toHaveCount(7);
    await expect(page.getByTestId("cost-ceiling")).toContainText(/\$\d/);
    await page.getByTestId("approve").click();
    await expect(page.getByTestId("decided")).toBeVisible();

    const done = await pollBatch(request, batch.id, allTerminal, 120_000);
    assertOnlyDuplicateFailures(done); // a take can collide with existing audio in the fake renderer
    expect(done.jobs).toHaveLength(7);
    expect(done.jobs.filter((j) => j.state === "completed").length).toBeGreaterThan(0);
  } finally {
    await agent.dispose();
  }
});

test("a second agent batch of two takes is rejected with a reason and its reservation is released", async ({ page, request, playwright, baseURL, agentToken }) => {
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${agentToken}` } });
  try {
    const mother = first(await composeRoots(request, 1, "m3a"));
    const batch = await agentBatch(agent, mother, 2);
    expect(batch.state).toBe("awaiting_confirmation");
    // the reservation is this batch's own figure: other specs' spending never changes it
    expect((await getBatch(request, batch.id)).cost.outstanding_micro_usd).toBeGreaterThan(0);

    await page.goto(`/human/confirm/${batch.id}`);
    await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
    await page.getByTestId("reject").click(); // a reason is required: nothing happens without one
    await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
    await page.getByTestId("decision-reason").fill("not now");
    await page.getByTestId("reject").click();
    await expect(page.getByTestId("decided")).toBeVisible();

    const after = await getBatch(request, batch.id);
    expect(after.approval.state).toBe("rejected");
    expect(after.cost.outstanding_micro_usd).toBe(0);
    expect(after.state).toBe("rejected");
    expect(after.jobs.every((j) => j.state !== "completed")).toBe(true);
  } finally {
    await agent.dispose();
  }
});
