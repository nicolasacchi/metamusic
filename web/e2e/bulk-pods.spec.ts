// matrix: chrome-only because it is a Make-tab generation flow of the Owner (bulk pods are an Owner, desktop decision)
// ops: estimate_generation, create_generation_human, list_pods, cancel_batch, get_provider_status
// M6 bulk (m6-bulk C398.6) against the real server with the fake pod backend `fake-pod` (server.ts SPEC_ENV: MM_POD_BACKEND=1 over MM_BACKEND=fake).
// Nothing is stubbed: every operation is answered by the Go handlers and counted by the served check. The e2e binary has no pod simulation behind
// the fake pod backend (that lives in the Go rig, TestAcceptance_Bulk_*), so no pod is ever created here: this spec pins the pages and the
// money path (estimate, refusal, confirm, cancel).
import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { draftsIntent } from "./bulkApi";

test.describe.configure({ mode: "serial", timeout: 120_000 });

async function generate(page: Page, request: APIRequestContext, drafts: number, seeds: number) {
  await draftsIntent(page, request, drafts, seeds);
  await page.goto("/human/generate");
}

test("the Jobs page lists live pods for the Owner and the provider status carries the pods block", async ({ page, request }) => {
  const listed = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/pods" && r.status() === 200);
  const provider = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/provider" && r.status() === 200);
  await page.goto("/jobs");
  await listed;
  await provider; // pods-off depends on the status: assert its absence only after it arrived
  await page.waitForTimeout(200);
  const section = page.getByTestId("jobs-pods");
  await expect(section).toBeVisible();
  await expect(section.getByTestId("pods-empty")).toContainText("No pod is running.");
  await expect(page.getByTestId("pods-off")).toHaveCount(0);
  const status = (await (await request.get("/api/provider")).json()) as { backends: { backend: string; pods?: { safety: string; alerts: string[] } }[] };
  const pod = status.backends.find((b) => b.pods !== undefined);
  expect(pod?.backend).toBe("fake-pod");
  expect(pod?.pods?.alerts).toEqual([]);
});

test("ten takes on a pod are refused in words, and the ordinary estimate comes back", async ({ page, request }) => {
  await generate(page, request, 1, 10);
  await expect(page.getByTestId("estimate")).toContainText("10 takes");
  const option = page.getByTestId("bulk-option");
  await expect(option).toBeVisible();
  await expect(option).toContainText("rented GPU pod");
  await page.getByTestId("bulk-toggle").check();
  await expect(page.getByTestId("estimate-error")).toContainText("at least 30");
  await expect(page.getByTestId("pod-estimate")).toHaveCount(0);
  await page.getByTestId("bulk-toggle").uncheck();
  await expect(page.getByTestId("estimate")).toContainText("10 takes");
  await expect(page.getByTestId("estimate-error")).toHaveCount(0);
});

test("a 30-take pod batch: the estimate shows the pod and its overhead, confirm opens the batch, cancel stops it", async ({ page, request }) => {
  await generate(page, request, 3, 10);
  await page.getByTestId("bulk-toggle").check();
  const card = page.getByTestId("estimate");
  await expect(card).toContainText("30 takes");
  await expect(card).toContainText("backend fake-pod");
  const pod = page.getByTestId("pod-estimate");
  await expect(pod).toBeVisible();
  await expect(page.getByTestId("pod-boot")).toContainText("min");
  await expect(page.getByTestId("pod-deadline")).toContainText("h");
  await expect(page.getByTestId("pod-overhead")).toContainText("part of Expected, Maximum and the Ceiling");
  await expect(page.getByTestId("pod-canary-note")).toContainText("always waits for the Owner");
  await expect(page.getByTestId("cost-ceiling")).toContainText(/\$\d/);

  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);
  const batchId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const cancelled = page.waitForResponse((r) => r.url().includes(`/api/batches/${batchId}/cancel`) && r.ok());
  await page.getByTestId("terminate-batch").click();
  await cancelled;
  await expect(page.getByTestId("batch-card")).toContainText("cancelled");

  const list = await request.get(`/api/pods?batch_id=${batchId}&state=all`);
  expect(list.ok()).toBe(true); // that stopping never recreates a pod is TestReconcilePods_CancelledBatchNeverRecreates (no pod row exists in this e2e)
});
