// matrix: chrome-only because the Critic Bench is a read-only table; the layout is shared with the other tables
// M6N-W1 (m6-night C388): the /critics page against the real server (the bench is computed from the seeded library).
import { expect as baseExpect, test } from "./fixtures";
import { openNav } from "./helpers";
import { siblings } from "./m2bApi";

const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ timeout: 90_000 });

// The bench has nothing to say until the Owner has judged a pair inside one lineage: rate the seeded sibling pair.
test.beforeAll(async ({ request }) => {
  const { root, child } = await siblings(request);
  expect((await request.put(`/api/tracks/${root.id}/rating`, { data: { stars: 5 } })).ok()).toBe(true);
  expect((await request.put(`/api/tracks/${child.id}/rating`, { data: { stars: 2 } })).ok()).toBe(true);
});

test("Critics is a Judge sub-view; it lists the critics with their state and the trust rule", async ({ page }) => {
  await page.goto("/judge");
  await openNav(page, "Critics");
  await expect(page).toHaveURL(/\/critics$/);
  await expect(page.getByRole("heading", { level: 1, name: "Critics" })).toBeVisible();
  await expect(page.getByTestId("critics-rule")).toContainText("independent pairs");
  const rows = page.getByTestId("critic-row");
  await expect(rows.first()).toBeVisible();
  expect(await rows.count()).toBeGreaterThan(0);
  await expect(rows.first().getByTestId("critic-state")).toBeVisible();
});

test("one critic opens its own view with the disagreements section", async ({ page }) => {
  await page.goto("/critics");
  const first = page.getByTestId("critic-row").first();
  await expect(first).toBeVisible();
  const id = await first.getAttribute("data-critic");
  await first.getByRole("link").first().click();
  await expect(page).toHaveURL(new RegExp(`/critics\\?critic=${encodeURIComponent(id ?? "").replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`));
  await expect(page.getByRole("heading", { level: 1, name: "Critics (one)" })).toBeVisible();
  await expect(page.getByTestId("critic-disagreements")).toBeVisible();
  await expect(page.getByTestId("critics-error")).toHaveCount(0);
});
