import { expect, test } from "@playwright/test";

test("backlog groups hold three drafts each", async ({ page }) => {
  await page.goto("/backlog");
  await expect(page.getByTestId("backlog-counts")).toContainText("13 drafts in 5 groups");
  const groups = page.getByTestId("backlog-group");
  await expect(groups).toHaveCount(5);
  for (const g of [1, 2, 3, 4]) {
    const grp = page.locator(`[data-testid="backlog-group"][data-group-key="synb_00${g}"]`);
    await expect(grp.getByTestId("draft-row")).toHaveCount(3);
  }
  // the varianti draft sits in its mother's lineage group
  await expect(page.locator('[data-testid="backlog-group"][data-group-key="syn-song-01"]').getByTestId("draft-row")).toHaveCount(1);
});

test("collection filter narrows to its drafts", async ({ page }) => {
  await page.goto("/backlog");
  await expect(page.getByTestId("draft-row")).toHaveCount(13);
  await page.getByTestId("backlog-collection").selectOption({ label: "pilot-synthetic (3)" });
  await expect(page).toHaveURL(/collection=pilot-synthetic/);
  await expect(page.getByTestId("draft-row")).toHaveCount(3);
  await expect(page.getByTestId("backlog-group")).toHaveCount(1);
  await expect(page.getByTestId("backlog-group")).toHaveAttribute("data-group-key", "synb_001");
});

test("draft detail shows lyrics structure and reference", async ({ page }) => {
  // The e2e profile carries no reference table, so the human-only reference block is injected
  // into the draft response (the block renders only when present).
  await page.route(/\/api\/drafts\/[0-9a-f-]+$/, async (route) => {
    const res = await route.fetch();
    const body = (await res.json()) as Record<string, unknown>;
    body.reference = { title: "Synthetic Reference", artist: "Synthetic Artist" };
    await route.fulfill({ response: res, json: body });
  });
  await page.goto("/backlog");
  await page.getByTestId("draft-row").filter({ hasText: "synb_001_v1" }).getByRole("link").first().click();
  await expect(page).toHaveURL(/\/backlog\/[0-9a-f-]+$/);
  await expect(page.getByTestId("draft-meta")).toBeVisible();
  await expect(page.getByTestId("draft-style")).toContainText("synthetic pop");
  const sections = page.getByTestId("draft-sections");
  await expect(sections).toContainText(/verse/i);
  await expect(sections).toContainText(/chorus/i);
  await expect(page.getByTestId("draft-reference")).toContainText("Synthetic Reference");
  await expect(page.getByTestId("draft-group").locator("li")).toHaveCount(3);
});
