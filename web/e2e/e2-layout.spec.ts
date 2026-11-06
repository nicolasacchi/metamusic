// matrix: chrome-only because the width is set by the test itself (360 px), whatever the device
import { expect, test, type Page } from "./fixtures";
import { trackByTitle } from "./helpers";
import { ACE, aceBase, ensureAdopted, overflowsX, setIntent, watchE2 } from "./e2Api";

// The narrowest phone: no page of the engine work scrolls sideways at 360 px.
test.use({ viewport: { width: 360, height: 740 } });
test.describe.configure({ timeout: 120_000 });

async function noOverflow(page: Page, what: string): Promise<void> {
  const { scroll, client } = await overflowsX(page);
  expect(scroll, `${what}: the page is ${scroll} px wide in a ${client} px window`).toBeLessThanOrEqual(client);
}

test.beforeEach(async ({ request, server }) => {
  await ensureAdopted(server, request);
});

test("the generate page with the picker and an ACE-Step estimate fits 360 px", async ({ page, request }) => {
  const watch = watchE2(page);
  const src = await trackByTitle(request, "Syn song 03");
  await setIntent(page, { mode: "port", engine: ACE, parent_track_id: src.id });
  await page.goto("/human/generate");
  await expect(page.getByTestId("engine-picker")).toBeVisible();
  await expect(page.getByTestId("estimate")).toBeVisible();
  await page.getByTestId("estimate").locator("summary", { hasText: "Takes" }).click();
  await expect(page.getByTestId("take-detail")).toBeVisible();
  await noOverflow(page, "generate");
  // the mode panel (the form before an estimate) too
  await setIntent(page, { mode: "repaint", engine: ACE, parent_track_id: src.id });
  await page.goto("/human/generate");
  await expect(page.getByTestId("mode-panel-repaint")).toBeVisible();
  await noOverflow(page, "generate, repaint panel");
  expect(watch.problems()).toEqual([]);
});

test("the Track page with the Repaint section open fits 360 px", async ({ page, request }) => {
  const watch = watchE2(page);
  const id = await aceBase(request, 30);
  await page.goto(`/tracks/${id}`);
  await expect(page.getByTestId("engine-actions")).toBeVisible();
  await noOverflow(page, "track");
  await page.getByTestId("action-repaint").click();
  await expect(page.getByTestId("range-select")).toBeVisible();
  await page.getByTestId("range-from").fill("4");
  await page.getByTestId("range-from").press("Tab");
  await page.getByTestId("range-to").fill("12");
  await page.getByTestId("range-to").press("Tab");
  await expect(page.getByTestId("range-info")).toContainText("(8 s)");
  await noOverflow(page, "track, repaint section");
  expect(watch.problems()).toEqual([]);
});

test("/engines fits 360 px (the matrix scrolls inside its own box)", async ({ page }) => {
  const watch = watchE2(page);
  await page.goto("/engines");
  await expect(page.getByTestId("engine-matrix")).toBeVisible();
  await noOverflow(page, "engines");
  expect(watch.problems()).toEqual([]);
});
