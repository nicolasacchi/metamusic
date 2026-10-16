// matrix: chrome-only because it is a pure data list (draft groups, filter, lyrics structure) with no device-dependent behaviour
import { expect, test } from "@playwright/test";

// M2a specs create drafts (editor, jsonl_upload origins) in the same library, so the seed counts are
// asserted per seed origin; the filter itself is part of what these tests exercise.
test("backlog groups hold three drafts each", async ({ page }) => {
  await page.goto("/backlog?origin=import_backlog");
  await expect(page.getByTestId("backlog-counts")).toContainText("12 drafts in 4 groups");
  const groups = page.getByTestId("backlog-group");
  await expect(groups).toHaveCount(4);
  for (const g of [1, 2, 3, 4]) {
    const grp = page.locator(`[data-testid="backlog-group"][data-group-key="synb_00${g}"]`);
    await expect(grp.getByTestId("draft-row")).toHaveCount(3);
  }
  // the varianti draft sits in its mother's lineage group
  await page.goto("/backlog?origin=varianti_import");
  await expect(page.locator('[data-testid="backlog-group"][data-group-key="syn-song-01"]').getByTestId("draft-row")).toHaveCount(1);
});

test("collection filter narrows to its drafts", async ({ page }) => {
  await page.goto("/backlog?origin=import_backlog");
  await expect(page.getByTestId("draft-row")).toHaveCount(12);
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
  await expect(page.getByTestId("draft-style-input")).toHaveValue(/synthetic pop/);
  const lyrics = page.getByTestId("draft-lyrics");
  await expect(lyrics).toHaveValue(/\[verse/i);
  await expect(lyrics).toHaveValue(/\[chorus/i);
  await expect(page.getByTestId("draft-reference")).toContainText("Synthetic Reference");
  await expect(page.getByTestId("draft-group").locator("li")).toHaveCount(3);
});
