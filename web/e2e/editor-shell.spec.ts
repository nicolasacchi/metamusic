// matrix: chrome-only because the Editor shell is asserted once, in the editor project (desktop Chrome signed in as bob)
import { expect, test } from "./fixtures";
import { idemKey, ownTracks } from "./helpers";
import { setIntent } from "./e2Api";
import { getBatch, type Batch } from "./m3Api";
import { asOwner } from "./u1Api";

// U1 C314.7: an Editor can ask for a generation but never decides, spends or sets caps. The request waits for the Owner, whose
// approval is made through the API (serial mode, one browser identity per project), never through a second browser context.
test.describe.configure({ mode: "serial", timeout: 150_000 });

let batchId = "";

test("More: Generate is there; Approvals, Spend, Tokens and Connectors are not, and no approvals badge or polling", async ({ page }) => {
  const calls: string[] = [];
  page.on("request", (r) => {
    const p = new URL(r.url()).pathname;
    if (p === "/api/batches") calls.push(p);
  });
  await page.goto("/more");
  await expect(page.getByTestId("identity")).toHaveText("bob");
  const main = page.getByRole("main");
  await expect(main.getByRole("link", { name: /^Generate\b/ })).toBeVisible();
  for (const label of ["Approvals", "Spend", "Tokens", "Connectors", "Audit", "Backups"]) await expect(main.getByRole("link", { name: new RegExp(`^${label}\\b`) }), label).toHaveCount(0);
  await expect(page.getByTestId("approvals-nav")).toHaveCount(0);
  await expect(page.getByTestId("approvals-badge")).toHaveCount(0);
  await page.waitForTimeout(500);
  expect(calls, "an Editor never polls the batch list").toEqual([]);
  for (const path of ["/human/spend", "/human/tokens"]) {
    await page.goto(path);
    await expect(page.getByRole("heading", { name: "No access" }), path).toBeVisible();
  }
});

test("the Editor cannot approve, reject, publish, set caps or reach the Owner's human API", async ({ request }) => {
  for (const [method, path, data] of [
    ["POST", "/human/api/batches/none/approve", {}],
    ["POST", "/human/api/batches/none/reject", { reason: "no" }],
    ["PUT", "/human/api/spend/caps", { monthly_micro_usd: 1_000_000 }],
    ["POST", "/human/api/records/none/publish", {}],
    ["GET", "/api/tokens", undefined],
    ["POST", "/human/api/generations", { confirmation_token: "none", rationale: "no" }],
  ] as const) {
    const r = await request.fetch(path, { method, ...(data === undefined ? {} : { data }), headers: { "Idempotency-Key": idemKey() } });
    expect(r.status(), `${method} ${path}`).toBe(403);
  }
});

test("a generation request from the Editor lands awaiting approval and shows no Approve control", async ({ page, request }, info) => {
  const [mother] = await ownTracks(request, info);
  await setIntent(page, { mode: "new_seed", parent_track_id: mother.id, n_seeds: 2 });
  await page.goto("/human/generate");
  await expect(page.getByTestId("estimate")).toBeVisible();
  await expect(page.getByTestId("request-note")).toBeVisible();
  await expect(page.getByTestId("confirm-button")).toContainText("Request approval");
  await page.getByTestId("confirm-button").click();
  await page.waitForURL(/\/batches\/[^/]+$/);
  batchId = new URL(page.url()).pathname.split("/").pop() ?? "";
  expect(batchId).not.toBe("");
  const b = await getBatch(request, batchId);
  expect(b.state).toBe("awaiting_confirmation");
  expect(b.approval.state).toBe("pending");
  await expect(page.getByTestId("review-waiting")).toBeVisible();
  await expect(page.getByTestId("review-approve")).toHaveCount(0);
  await expect(page.getByRole("link", { name: /Review & approve/ })).toHaveCount(0);
});

test("the Owner approves the request through the API and the batch starts", async ({ request, playwright, baseURL }) => {
  expect(batchId, "the previous test made the request").not.toBe("");
  const owner = await asOwner(playwright, baseURL);
  try {
    // the Editor's own approve is refused (above); the Owner's goes through
    expect((await request.post(`/human/api/batches/${batchId}/approve`, { data: {}, headers: { "Idempotency-Key": idemKey() } })).status()).toBe(403);
    const r = await owner.post(`/human/api/batches/${batchId}/approve`, { data: {}, headers: { "Idempotency-Key": idemKey() } });
    expect(r.ok(), await r.text()).toBe(true);
    await expect
      .poll(async () => ((await (await request.get(`/api/batches/${batchId}`)).json()) as Batch).state, { timeout: 60_000 })
      .not.toBe("awaiting_confirmation");
  } finally {
    await owner.dispose();
  }
});
