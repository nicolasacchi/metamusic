// matrix: @mobile because the three-click variant flow is run from the phone
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { apiJSON } from "./helpers";
import { allTerminal, assertOnlyDuplicateFailures, composeRoots, first, onlyDuplicates, pollBatch } from "./m3Api";

// Runs on desktop Chrome, the Pixel 7 and the iPhone 15 projects, one after the other (playwright.config.ts). Each run
// composes its own mother, so the lineage it checks holds exactly its three children. A take that collides with
// existing audio in the simulated renderer (see DUPLICATE_AUDIO in m3Api.ts) makes the run start again on a new mother.

test.describe.configure({ timeout: 240_000 });

/** One pass of the three-click flow; false when the fake renderer collided and the pass has to be repeated. */
async function threeClicks(page: Page, request: APIRequestContext): Promise<boolean> {
  const mother = first(await composeRoots(request, 1, "m3v"));

  await page.goto(`/tracks/${mother}`);
  await page.getByTestId("generate-button").locator("summary").click();
  await page.getByTestId("variant-new_seed").click();

  // the generate page prices the intent by itself
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("estimate")).toBeVisible();
  await expect(page.getByTestId("estimate")).toContainText("3 takes");
  await expect(page.getByTestId("cost-max")).toContainText(/\$\d/);
  await page.getByTestId("estimate").locator("summary", { hasText: "Takes" }).click();
  const takes = page.getByTestId("estimate-takes").locator("tbody tr");
  await expect(takes).toHaveCount(3);
  const seeds = await takes.locator("td:nth-child(3)").allTextContents();
  expect(new Set(seeds).size).toBe(3);

  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);
  const batchId = new URL(page.url()).pathname.split("/").pop() ?? "";

  const rows = page.getByTestId("batch-jobs").getByTestId("job-row");
  await expect(rows).toHaveCount(3);
  const batch = await pollBatch(request, batchId, allTerminal, 120_000);
  assertOnlyDuplicateFailures(batch);
  if (!batch.jobs.every((j) => j.state === "completed")) {
    expect(onlyDuplicates(batch)).toBe(true);
    return false;
  }
  await expect(rows.and(page.locator('[data-state="completed"]'))).toHaveCount(3, { timeout: 30_000 });
  // the request ids carry the seeds
  for (const j of batch.jobs) expect(j.request_id.endsWith(`-s${j.seed}`)).toBe(true);
  const children = batch.jobs.map((j) => j.track_id ?? "");
  expect(children.every((c) => c !== "")).toBe(true);

  // the lineage lists the three children, each with its cost
  await page.goto(`/lineage/${mother}`);
  await expect(page.getByTestId("lineage-node")).toHaveCount(4);
  for (const c of children) {
    const node = page.locator(`[data-testid="lineage-node"][data-id="${c}"]`);
    await expect(node).toBeVisible();
    await expect(node.getByTestId("lineage-cost")).toContainText(/\$\d/);
    await expect(node.getByTestId("lineage-operator")).toContainText("reroll seed");
  }

  // a child's page shows how it came to be and offers the non-blind duel with its parent
  await page.goto(`/tracks/${first(children)}`);
  await expect(page.getByTestId("provenance")).toBeVisible();
  await expect(page.getByTestId("prov-operator")).toContainText("reroll seed");
  await expect(page.getByTestId("prov-cost")).toContainText(/\$\d/);
  await page.getByTestId("prov-ab-parent").click();
  await expect(page).toHaveURL(/\/duels\/[0-9a-f-]+$/);
  const duelId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const duel = await apiJSON<{ blind: boolean }>(request, "GET", `/api/duels/${duelId}`);
  expect(duel.blind).toBe(false);
  return true;
}

test("three-click new-seed variant: estimate, spend, three completed children with costs and provenance", async ({ page, request }) => {
  for (let pass = 0; pass < 4; pass++) if (await threeClicks(page, request)) return;
  throw new Error("every pass collided with existing audio in the fake renderer");
});
