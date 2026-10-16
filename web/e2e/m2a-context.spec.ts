// matrix: chrome-only because it only checks a character count (the clipboard is not read)
import { expect, test } from "@playwright/test";
import { trackByTitle } from "./helpers";

// The clipboard is not read: Firefox grants Playwright no clipboard permission. The displayed count is
// the length of the bundle the button fetched (get_track_context).
test("Copy agent context shows a character count within the 24000 budget", async ({ page, request }) => {
  const t = await trackByTitle(request, "Syn song 03");
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("copy-context").click();
  const chars = page.getByTestId("context-chars");
  await expect(chars).toBeVisible();
  const n = Number(await chars.getAttribute("data-chars"));
  expect(n).toBeGreaterThan(200);
  expect(n).toBeLessThanOrEqual(24000);
  await expect(chars).toContainText(`${n} chars`);
});
