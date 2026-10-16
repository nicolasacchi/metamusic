// matrix: @mobile because live job following and termination are watched from the phone
import { expect, test, type Page } from "./fixtures";
import { composeRoots, first, getBatch, pollBatch, startSlowBatch } from "./m3Api";

// Desktop Chrome, then the Pixel 7 and the iPhone 15 (one project after the other: the fake endpoint has two workers
// and these specs hold them with "fake:slow=60" takes until they terminate them). Rows are found by their own job and
// batch ids; other specs' jobs on the page are never counted.
test.describe.configure({ mode: "serial", timeout: 150_000 });

// A job is listed under "Active" and, being among the newest, under "Recent jobs" too: the first row is the live one.
const jobRow = (page: Page, id: string) => page.locator('[data-testid="job-row"]').filter({ has: page.locator(`a[href="/jobs/${id}"]`) }).first();
const batchCard = (page: Page, id: string) => page.locator('[data-testid="batch-card"]').filter({ has: page.locator(`a[href="/batches/${id}"]`) });

test("a running take is listed, followed live on its page and terminated without a reload", async ({ page, request }) => {
  const mother = first(await composeRoots(request, 1, "m3j"));
  const batch = await startSlowBatch(request, mother, 1);
  const job = first(batch.jobs).id;

  await page.goto("/jobs");
  const row = jobRow(page, job);
  await expect(row).toBeVisible();
  // it holds a worker: it leaves "queued" and shows up as starting or generating
  await expect(row).toHaveAttribute("data-state", /^(starting|generating)$/, { timeout: 60_000 });
  await expect(page.getByTestId("live-jobs")).toBeVisible();

  await row.getByRole("link").first().click();
  await expect(page).toHaveURL(new RegExp(`/jobs/${job}$`));
  await expect(page.getByTestId("job-state")).toHaveText(/Starting|Generating/);
  await page.evaluate(() => ((window as unknown as { __noReload?: boolean }).__noReload = true));
  await page.getByTestId("terminate-job").click();
  await expect(page.getByTestId("job-state")).toHaveText("Cancelled", { timeout: 60_000 });
  // the document was not reloaded: the page followed the job
  expect(await page.evaluate(() => (window as unknown as { __noReload?: boolean }).__noReload)).toBe(true);
  await expect(page.getByTestId("terminate-job")).toHaveCount(0);
  expect(first((await getBatch(request, batch.id)).jobs).state).toBe("cancelled");
});

test("a whole batch is terminated; month spend, ledger and provider tabs render", async ({ page, request }) => {
  const mother = first(await composeRoots(request, 1, "m3j"));
  const batch = await startSlowBatch(request, mother, 2);
  expect(batch.jobs).toHaveLength(2);

  await page.goto("/jobs");
  const card = batchCard(page, batch.id);
  await expect(card).toHaveCount(1);
  await card.getByTestId("terminate-batch").click();
  for (const j of batch.jobs) await expect(jobRow(page, j.id)).toHaveAttribute("data-state", "cancelled", { timeout: 60_000 });
  const done = await pollBatch(request, batch.id, (b) => b.jobs.every((j) => j.state === "cancelled"));
  expect(done.state).toBe("finished");
  expect(done.jobs.map((j) => j.error_code)).toEqual(["CANCELLED_BY_USER", "CANCELLED_BY_USER"]);

  await page.getByTestId("tab-spend").click();
  await expect(page.getByTestId("month")).toHaveValue(/^\d{4}-\d{2}$/);
  await expect(page.getByTestId("spend-totals")).toContainText(/Settled \$/);
  await page.getByTestId("tab-ledger").click();
  await expect(page.getByTestId("ledger").locator("tbody tr").first()).toBeVisible();
  await page.getByTestId("tab-provider").click();
  await expect(page.getByTestId("provider-tab")).toContainText("Backend fake");
});
