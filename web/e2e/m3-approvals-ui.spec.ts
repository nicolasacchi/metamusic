import { expect, test, type APIRequestContext, type Page, type PlaywrightWorkerArgs } from "@playwright/test";
import { BASE_URL } from "./env";
import { apiJSON, uniq } from "./helpers";
import { agentCreateHeaders, composeRoots, first, getBatch, type Batch, type Estimate } from "./m3Api";

// Desktop Chrome, Pixel 7 and iPhone 15. Pending approvals are discoverable inside the app: nav badge, Jobs section,
// link to the confirm page. Counts are compared with the server's own pending list (other specs may leave batches
// pending), never with absolute numbers. Desktop approves (the fake renderer runs it); the phones reject, so that the
// two fake workers stay free for the m3-variant and m3-jobs specs of the same project.

test.describe.configure({ mode: "serial", timeout: 150_000 });

// The spec's own agent: a dedicated token with its own daily budget, so it never depends on what the shared
// e2e-agent token spent in earlier specs of the same set.
async function ownAgent(request: APIRequestContext, playwright: PlaywrightWorkerArgs["playwright"]): Promise<{ ctx: APIRequestContext; name: string }> {
  const name = uniq("e2e-approvals");
  const tok = await apiJSON<{ id: string; token: string }>(request, "POST", "/human/api/tokens", { name, scopes: ["read", "write", "generate"] }, true);
  await apiJSON(request, "PUT", "/human/api/spend/caps", { tokens: [{ token_id: tok.id, auto_approve_takes: 1, auto_approve_micro_usd: null, daily_budget_micro_usd: 50_000_000 }] });
  return { ctx: await playwright.request.newContext({ baseURL: BASE_URL, extraHTTPHeaders: { Authorization: `Bearer ${tok.token}` } }), name };
}

// Whatever happens, the spec's batch must not stay pending for the specs that count on a quiet queue.
let createdBatch: string | null = null;
test.afterEach(async ({ request }) => {
  if (!createdBatch) return;
  const id = createdBatch;
  createdBatch = null;
  const b = await request.get(`/api/batches/${id}`);
  if (b.ok() && ((await b.json()) as Batch).approval.state === "pending") {
    await request.fetch(`/human/api/batches/${id}/reject`, { method: "POST", data: { reason: "e2e cleanup" }, headers: { "Idempotency-Key": uniq("cleanup") } });
  }
});

interface Own {
  batch: Batch;
  agentName: string;
  dispose: () => Promise<void>;
}

async function ownPendingBatch(request: APIRequestContext, playwright: PlaywrightWorkerArgs["playwright"]): Promise<Own> {
  const { ctx: agent, name: agentName } = await ownAgent(request, playwright);
  const mother = first(await composeRoots(request, 1, "m3u"));
  const er = await agent.post(`/api/tracks/${mother}/variants/estimate`, { data: { mode: "new_seed", n_seeds: 2 } });
  expect(er.ok(), await er.text()).toBe(true);
  const e = (await er.json()) as Estimate;
  expect(e.requires_human_approval).toBe(true);
  const cr = await agent.post("/api/generations", { data: { confirmation_token: e.confirmation_token, rationale: "e2e approvals ui" }, headers: agentCreateHeaders() });
  expect(cr.status(), await cr.text()).toBe(201);
  const batch = (await cr.json()) as Batch;
  createdBatch = batch.id;
  expect(batch.state).toBe("awaiting_confirmation");
  return { batch, agentName, dispose: () => agent.dispose() };
}

async function decide(page: Page, project: string): Promise<void> {
  await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
  if (project === "m3-chrome") {
    await page.getByTestId("approve").click();
  } else {
    await page.getByTestId("decision-reason").fill("e2e: not now");
    // on phones the sticky player bar can cover the button and Playwright's hit test never settles: click it in the DOM
    await page.getByTestId("reject").evaluate((el) => (el as HTMLButtonElement).click());
  }
  await expect(page.getByTestId("decided")).toBeVisible();
}

async function pendingCount(request: APIRequestContext): Promise<number> {
  const r = await request.get("/api/batches?state=awaiting_confirmation&limit=50");
  expect(r.ok()).toBe(true);
  return ((await r.json()) as { items: { approval: { state: string } }[] }).items.filter((b) => b.approval.state === "pending").length;
}

test("a pending agent batch shows in the nav badge, the hub and Jobs, links to the confirm page and leaves the lists when decided", async ({ page, request, playwright }, info) => {
  const { batch, agentName, dispose } = await ownPendingBatch(request, playwright);
  try {
    // the badge in the main navigation, also on a narrow viewport: other specs may decide batches at the same time,
    // so the count is only checked against a fresh server read taken in the same poll
    await page.goto("/");
    const badge = page.getByTestId("approvals-badge");
    await expect(badge).toBeVisible();
    expect(Number(await badge.innerText())).toBeGreaterThanOrEqual(1);
    await page.screenshot({ path: `test-results/approvals-badge-${info.project.name}.png` });

    // the badge leads to the human hub, whose Approvals section lists the batch
    await page.getByTestId("approvals-nav").click();
    await expect(page).toHaveURL(/\/human\/$/);
    const row = page.locator(`[data-testid="approval-row"][data-batch="${batch.id}"]`);
    await expect(row).toBeVisible();
    await expect(row.getByTestId("approval-by")).toHaveText(agentName);
    await expect(row.getByTestId("approval-takes")).toHaveText("2 takes");
    await expect(row.getByTestId("approval-ceiling")).toContainText(/\$\d/);
    await expect(row.getByTestId("approval-reason").first()).toBeVisible();

    // the Jobs page carries the same section
    await page.goto("/jobs");
    await expect(row).toBeVisible();
    await page.screenshot({ path: `test-results/approvals-jobs-${info.project.name}.png` });

    // the batch page carries the same link
    await page.goto(`/batches/${batch.id}`);
    await expect(page.getByTestId("review-approve").getByRole("link")).toHaveAttribute("href", `/human/confirm/${batch.id}`);

    // the Jobs link opens the confirm page
    await page.goto("/jobs");
    await row.getByTestId("approval-link").click();
    await expect(page).toHaveURL(new RegExp(`/human/confirm/${batch.id}$`));
    await decide(page, info.project.name);
    expect((await getBatch(request, batch.id)).approval.state).not.toBe("pending");

    // decided: this batch's row is gone, and the badge agrees with a fresh read of the server (absent at zero)
    await page.goto("/jobs");
    await expect(row).toHaveCount(0);
    await expect
      .poll(async () => {
        const fresh = await pendingCount(request);
        const text = (await page.getByTestId("approvals-badge").count()) === 0 ? "0" : await page.getByTestId("approvals-badge").innerText();
        if (text === String(fresh)) return true;
        await page.reload();
        return false;
      }, { timeout: 30_000 })
      .toBe(true);
  } finally {
    await dispose();
  }
});

test("/human is the hub: both spellings land on it, it lists the batch, the link decides it and the batch moves to recent decisions", async ({ page, request, playwright }, info) => {
  // the bare path only redirects (the edge's two-factor rule is ^/human/); the redirect itself reveals nothing
  const bare = await request.get("/human", { maxRedirects: 0 });
  expect(bare.status()).toBe(308);
  expect(new URL(bare.headers()["location"] ?? "", "http://x").pathname).toBe("/human/");

  const { batch, dispose } = await ownPendingBatch(request, playwright);
  try {
    await page.goto("/human");
    await expect(page).toHaveURL(/\/human\/$/);
    await expect(page.getByRole("heading", { name: "Human" })).toBeVisible();
    for (const t of ["Generate", "Spend caps", "Tokens", "Devices"]) await expect(page.getByTestId("human-card").filter({ hasText: t })).toHaveCount(1);
    await page.goto("/human/");
    const row = page.locator(`[data-testid="approval-row"][data-batch="${batch.id}"]`);
    await expect(row).toBeVisible();
    await page.screenshot({ path: `test-results/human-hub-${info.project.name}.png` });

    await row.getByTestId("approval-link").click();
    await expect(page).toHaveURL(new RegExp(`/human/confirm/${batch.id}$`));
    await decide(page, info.project.name);

    await page.goto("/human/");
    await expect(row).toHaveCount(0);
    await expect(page.locator(`[data-testid="decision-row"][data-batch="${batch.id}"]`)).toBeVisible();
  } finally {
    await dispose();
  }
});
