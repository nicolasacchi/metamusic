// matrix: chrome-only because this is a request-count and console check, independent of the device
import { expect, test } from "./fixtures";
import { trackByTitle } from "./helpers";
import { watchE2 } from "./e2Api";

// A YuE2 take page and a YuE2 variant make no list_engines call and log nothing, with the second engine enabled on the server.
// The call is made only when the user opens an engine action or presses "Change engine".

test("the Track page of a YuE2 take makes no list_engines call and logs no console error", async ({ page, request }) => {
  const watch = watchE2(page, true);
  const t = await trackByTitle(request, "Syn song 03");
  await page.goto(`/tracks/${t.id}`);
  await expect(page.getByTestId("track-title")).toBeVisible();
  await expect(page.getByTestId("engine-actions")).toBeVisible();
  await page.waitForLoadState("networkidle");
  expect(watch.engineCalls()).toBe(0);
  expect(watch.problems()).toEqual([]);
});

test("a YuE2 variant is priced without asking for the engines until Change engine is pressed", async ({ page, request }) => {
  const watch = watchE2(page);
  const t = await trackByTitle(request, "Syn song 03");
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("generate-button").locator("summary").click();
  await page.getByTestId("variant-new_seed").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("estimate")).toContainText("engine yue2");
  await page.waitForLoadState("networkidle");
  expect(watch.engineCalls()).toBe(0);
  await page.getByTestId("change-engine").click();
  await expect(page.getByTestId("engine-picker")).toBeVisible();
  expect(watch.engineCalls()).toBe(1);
  expect(watch.problems()).toEqual([]);
});
