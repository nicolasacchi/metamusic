// matrix: chrome-only because the reference flow shares its steps with e2-repaint (which runs on the phones); this one adds the default window and the render
import { expect, test } from "./fixtures";
import { apiJSON } from "./helpers";
import { allTerminal, first, pollBatch } from "./m3Api";
import { aceBase, aceReference, ensureAdopted, watchE2 } from "./e2Api";

test.describe.configure({ mode: "serial", timeout: 150_000 });

let source = "";
test.beforeEach(async ({ request, server }) => {
  await ensureAdopted(server, request);
  if (!source) source = await aceBase(request, 40);
});

test("Render with this as reference: the window defaults to the last 20 s, the estimate shows it, the render follows it", async ({ page, request }) => {
  const watch = watchE2(page);
  await page.goto(`/tracks/${source}`);
  await expect(page.getByTestId("engine-actions")).toBeVisible();
  await page.getByTestId("action-reference").click();
  await expect(page.getByTestId("range-select")).toBeVisible();
  await expect(page.getByTestId("action-reason")).toHaveCount(0);
  await expect(page.getByTestId("range-info")).toContainText("at most 30 s");
  // nothing selected: the server's default (the last 20 s) applies
  await page.getByTestId("action-continue").click();

  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("intent-summary")).toContainText("reference on ACE-Step");
  const card = page.getByTestId("estimate");
  await expect(card).toBeVisible();
  await card.locator("summary", { hasText: "Takes" }).click();
  await expect(page.getByTestId("take-detail")).toContainText("reference 20–40 s");
  await expect(page.getByTestId("hint-reference_not_reproducible")).toBeVisible();

  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);
  const batchId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const batch = await pollBatch(request, batchId, allTerminal, 120_000);
  const job = first(batch.jobs);
  if (job.state === "completed") {
    const tr = await apiJSON<{ recipe: { acestep: { task: string; ref: { track_id: string; from_s: number; to_s: number } } } }>(request, "GET", `/api/tracks/${job.track_id}`);
    expect(tr.recipe.acestep.ref).toMatchObject({ track_id: source, from_s: 20, to_s: 40 });
  } else expect(job.error_code).toBe("DUPLICATE_AUDIO"); // the simulated renderer, not the product
  expect(watch.problems()).toEqual([]);
});

test("an explicit window is used as selected and must fit the engine's limit", async ({ page }) => {
  const watch = watchE2(page);
  await page.goto(`/tracks/${source}`);
  await page.getByTestId("action-reference").click();
  await expect(page.getByTestId("range-select")).toBeVisible();
  await page.getByTestId("range-from").fill("5");
  await page.getByTestId("range-from").press("Tab");
  await page.getByTestId("range-to").fill("45");
  await page.getByTestId("range-to").press("Tab");
  await expect(page.getByTestId("range-problem")).toContainText("at most 30 s");
  await expect(page.getByTestId("action-continue")).toBeDisabled();
  await page.getByTestId("range-to").fill("15");
  await page.getByTestId("range-to").press("Tab");
  await expect(page.getByTestId("action-continue")).toBeEnabled();
  await page.getByTestId("action-continue").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await page.getByTestId("estimate").locator("summary", { hasText: "Takes" }).click();
  await expect(page.getByTestId("take-detail")).toContainText("reference 5–15 s");
  expect(watch.problems()).toEqual([]);
});

test("the reference render is a child of the take it followed and names the window", async ({ request }) => {
  const child = await aceReference(request, source, 10, 30);
  const tr = await apiJSON<{ lineage: { parent_track_id: string | null }; recipe: { acestep: { ref: { track_id: string; from_s: number; to_s: number } } } }>(request, "GET", `/api/tracks/${child}`);
  expect(tr.recipe.acestep.ref).toMatchObject({ track_id: source, from_s: 10, to_s: 30 });
  expect(tr.lineage.parent_track_id).toBe(source);
});
