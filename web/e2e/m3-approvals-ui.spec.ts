import { expect, test, type APIRequestContext } from "@playwright/test";
import { BASE_URL } from "./env";
import { agentCreateHeaders, composeRoots, first, getBatch, type Batch, type Estimate } from "./m3Api";

// Desktop Chrome, Pixel 7 and iPhone 15. Pending approvals are discoverable inside the app: nav badge, Jobs section,
// link to the confirm page. Counts are compared with the server's own pending list (other specs may leave batches
// pending), never with absolute numbers. Desktop approves (the fake renderer runs it); the phones reject, so that the
// two fake workers stay free for the m3-variant and m3-jobs specs of the same project.

test.describe.configure({ mode: "serial", timeout: 150_000 });

async function pendingCount(request: APIRequestContext): Promise<number> {
  const r = await request.get("/api/batches?state=awaiting_confirmation&limit=50");
  expect(r.ok()).toBe(true);
  return ((await r.json()) as { items: { approval: { state: string } }[] }).items.filter((b) => b.approval.state === "pending").length;
}

test("a pending agent batch shows in the nav badge and on Jobs, links to the confirm page and leaves the badge when decided", async ({ page, request, playwright }, info) => {
  const token = process.env.MM_E2E_AGENT_TOKEN;
  if (!token) throw new Error("the global setup did not create the agent token");
  const agent = await playwright.request.newContext({ baseURL: BASE_URL, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
  try {
    const mother = first(await composeRoots(request, 1, "m3u"));
    const er = await agent.post(`/api/tracks/${mother}/variants/estimate`, { data: { mode: "new_seed", n_seeds: 2 } });
    expect(er.ok(), await er.text()).toBe(true);
    const e = (await er.json()) as Estimate;
    expect(e.requires_human_approval).toBe(true);
    const cr = await agent.post("/api/generations", { data: { confirmation_token: e.confirmation_token, rationale: "e2e approvals ui" }, headers: agentCreateHeaders() });
    expect(cr.status(), await cr.text()).toBe(201);
    const batch = (await cr.json()) as Batch;
    expect(batch.state).toBe("awaiting_confirmation");

    // the badge in the main navigation, also on a narrow viewport
    const n = await pendingCount(request);
    expect(n).toBeGreaterThan(0);
    await page.goto("/");
    const badge = page.getByTestId("approvals-badge");
    await expect(badge).toBeVisible();
    await expect(badge).toHaveText(String(n));
    await page.screenshot({ path: `test-results/approvals-badge-${info.project.name}.png` });

    // the badge leads to the Jobs page, whose Approvals section lists the batch
    await page.getByTestId("approvals-nav").click();
    await expect(page).toHaveURL(/\/jobs$/);
    const row = page.locator(`[data-testid="approval-row"][data-batch="${batch.id}"]`);
    await expect(row).toBeVisible();
    await expect(row.getByTestId("approval-by")).toHaveText("e2e-agent");
    await expect(row.getByTestId("approval-takes")).toHaveText("2 takes");
    await expect(row.getByTestId("approval-ceiling")).toContainText(/\$\d/);
    await expect(row.getByTestId("approval-reason").first()).toBeVisible();
    await page.screenshot({ path: `test-results/approvals-jobs-${info.project.name}.png` });

    // the batch page carries the same link
    await page.goto(`/batches/${batch.id}`);
    await expect(page.getByTestId("review-approve").getByRole("link")).toHaveAttribute("href", `/human/confirm/${batch.id}`);

    // the Jobs link opens the confirm page
    await page.goto("/jobs");
    await row.getByTestId("approval-link").click();
    await expect(page).toHaveURL(new RegExp(`/human/confirm/${batch.id}$`));
    await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
    if (info.project.name === "m3-chrome") {
      await page.getByTestId("approve").click();
    } else {
      await page.getByTestId("decision-reason").fill("e2e: not now");
      await page.getByTestId("reject").click();
    }
    await expect(page.getByTestId("decided")).toBeVisible();
    expect((await getBatch(request, batch.id)).approval.state).not.toBe("pending");

    // decided: the batch is gone from the section and the count dropped by one
    await page.goto("/jobs");
    await expect(row).toHaveCount(0);
    const after = await pendingCount(request);
    expect(after).toBe(n - 1);
    if (after === 0) await expect(page.getByTestId("approvals-badge")).toHaveCount(0);
    else await expect(page.getByTestId("approvals-badge")).toHaveText(String(after));
  } finally {
    await agent.dispose();
  }
});
