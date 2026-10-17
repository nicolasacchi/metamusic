// matrix: @mobile because touch targets (44 px) and the phone library list above the fold are only meaningful on a phone
import { expect, test, type Page } from "./fixtures";
import { ownTracks, smallTargets, spaGoto } from "./helpers";
import { createDuel, siblings } from "./m2bApi";

const isPhone = (page: Page) => (page.viewportSize()?.width ?? 1280) < 768;

async function expectTouchable(page: Page, where: string) {
  const small = await smallTargets(page);
  expect.soft(small, `${where}: targets under 44x44`).toEqual([]);
}

test("library shows music above the fold", async ({ page }) => {
  test.skip(!isPhone(page), "phone layout");
  await page.goto("/");
  const row = page.getByTestId("track-row").first();
  await expect(row).toBeVisible();
  const box = await row.boundingBox();
  expect(box?.y ?? 9999, "the first track row starts above y=200").toBeLessThan(200);
  expect(box?.height ?? 0).toBeGreaterThanOrEqual(63);
  await expect(page.getByTestId("track-table")).toHaveCount(0);
  await expect(page.getByTestId("filters-open")).toBeVisible();
});

test("touch targets: Library, New, Track, Backlog and Duel", async ({ page, request }, info) => {
  test.skip(!isPhone(page), "phone layout");
  const [t] = await ownTracks(request, info);
  const { root, child } = await siblings(request);
  const duel = await createDuel(request, root.id, child.id);

  await page.goto("/");
  await expect(page.getByTestId("track-row").first()).toBeVisible();
  await expectTouchable(page, "library");
  await page.getByTestId("filters-open").click();
  await expect(page.getByTestId("filters-sheet")).toBeVisible();
  await expectTouchable(page, "library filters sheet");
  await page.getByTestId("filters-sheet").getByRole("button", { name: "Done" }).click();

  await page.goto("/new");
  await expect(page.getByTestId("topbar-title")).toHaveText("New");
  await page.waitForLoadState("networkidle");
  await expectTouchable(page, "new");

  await page.goto(`/tracks/${t.id}`);
  await expect(page.getByTestId("track-judge")).toBeVisible();
  await expect(page.getByTestId("status-chips")).toBeVisible();
  await expectTouchable(page, "track");

  await page.goto("/backlog");
  await expect(page.getByTestId("backlog-counts")).toBeVisible();
  await expectTouchable(page, "backlog");

  await spaGoto(page, `/duels/${duel.id}`);
  await expect(page.getByTestId("ab-play")).toBeVisible({ timeout: 20_000 });
  await expectTouchable(page, "duel");
});
