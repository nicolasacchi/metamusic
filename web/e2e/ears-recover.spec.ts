// matrix: chrome-only because it is a server-side data flow (analyze, recover, draft); the panel is the shared Track card
import { expect, test, type APIRequestContext } from "./fixtures";
import { apiJSON } from "./helpers";

// Recover a recipe from a reference take (M5b W2): Analyze through the ordinary estimate, then "Create draft from transcript" makes
// a draft (no spend, no job) whose lyrics are what the recogniser heard. The draft's render goes through the ordinary estimate.

test.describe.configure({ timeout: 120_000 });

interface Row {
  id: string;
  title: string;
}

async function lossless(request: APIRequestContext): Promise<Row> {
  const r = await apiJSON<{ items: Row[] }>(request, "GET", "/api/tracks?limit=50&recipe_known=false&lossy=false");
  const rows = [...r.items].sort((a, b) => (a.title < b.title ? -1 : 1));
  if (rows.length === 0) throw new Error("the e2e profile has lossless reference takes");
  return rows[0]!;
}

const totals = async (request: APIRequestContext) => (await apiJSON<{ totals: { settled_micro_usd: number; outstanding_micro_usd: number } }>(request, "GET", "/api/spend")).totals;

test("a reference take is analysed, then Create draft from transcript makes a draft without any spend", async ({ page, request }) => {
  const ref = await lossless(request);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));

  await page.goto(`/tracks/${ref.id}`);
  const panel = page.getByTestId("recover");
  await expect(panel.getByTestId("recover-needs-check")).toBeVisible();
  await expect(panel.getByTestId("recover-button")).toHaveCount(0);

  // Analyze is the ordinary estimate and confirmation
  await panel.getByTestId("analyze-button").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("estimate")).toBeVisible();
  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[^/]+$/);
  await expect.poll(async () => (await request.get(`/api/tracks/${ref.id}/tier-b`)).status(), { timeout: 60_000, intervals: [500, 1000] }).toBe(200);

  await page.goto(`/tracks/${ref.id}`);
  await expect(page.getByTestId("recover-button")).toBeVisible();
  await expect(page.getByTestId("recover-needs-check")).toHaveCount(0);
  const spendBefore = await totals(request);
  const jobsBefore = (await apiJSON<{ items: unknown[] }>(request, "GET", "/api/jobs?limit=100")).items.length;
  await page.getByTestId("recover-style").fill("italian vocals, soft, 90 BPM");

  // the e2e fake recogniser hears an invented Italian lyric in the reference, so recover_recipe really answers (served to the SPA):
  // the draft opens, and nothing was spent or enqueued
  await page.getByTestId("recover-button").click();
  await expect(page).toHaveURL(/\/backlog\/[^/]+$/);
  expect((await apiJSON<{ items: unknown[] }>(request, "GET", "/api/jobs?limit=100")).items.length).toBe(jobsBefore);
  expect(await totals(request)).toEqual(spendBefore);
  expect(errors).toEqual([]);
});

test("a take that is not a reference has no Recover panel", async ({ page, request }) => {
  const all = await apiJSON<{ items: Row[] }>(request, "GET", "/api/tracks?limit=1&recipe_known=true");
  await page.goto(`/tracks/${all.items[0]!.id}`);
  await expect(page.getByTestId("recipe-unknown")).toHaveCount(0);
  await expect(page.getByTestId("recover")).toHaveCount(0);
});
