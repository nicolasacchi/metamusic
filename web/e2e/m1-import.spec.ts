// matrix: chrome-only because it is an admin counts page with no device-dependent behaviour
import { expect, test } from "./fixtures";
import { SEED_AUDIO_FILES } from "./env";

test("import page shows the seed run with counts and paged sections", async ({ page }) => {
  await page.goto("/import");
  await expect(page.getByTestId("import-state")).toHaveText("completed");
  await expect(page.getByTestId("import-errors")).toHaveText("errors: 0");
  await expect(page.getByTestId("import-warnings")).toBeVisible();
  const summary = page.getByTestId("import-summary");
  await expect(summary.locator('[data-count="audio_files"]')).toHaveText(`audio_files: ${SEED_AUDIO_FILES}`);
  await expect(summary.locator('[data-count="collections"]')).toHaveText("collections: 1");

  // paged sections: each renders a (possibly empty) page plus the pager
  for (const section of ["errors", "unmatched", "ambiguous", "audio"]) {
    const tab = page.locator(`nav[aria-label="Sections"] button[data-section="${section}"]`);
    // dispatchEvent: on the iPhone 15 viewport the page overflows horizontally by a few px (the player
    // footer is wider than the viewport), which skews Playwright's pointer hit-testing for real clicks.
    await tab.dispatchEvent("click");
    await expect(page.getByTestId("import-items")).toHaveAttribute("data-section", section);
    await expect(page.getByTestId("pager")).toBeVisible();
  }
  const items = page.getByTestId("import-items").locator("li");
  await expect(items).toHaveCount(SEED_AUDIO_FILES); // audio section: one line per matched file
  // no absolute filesystem paths are rendered anywhere on the page
  await expect(page.locator("main")).not.toContainText(/(^|\s)\/(tmp|home|var|srv)\//);
});
