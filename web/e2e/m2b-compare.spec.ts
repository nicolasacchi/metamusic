import { expect, test } from "@playwright/test";
import { siblings } from "./m2bApi";

test("compare of the two siblings shows the LUFS delta row and relation siblings", async ({ page, request }) => {
  const { root, child } = await siblings(request);
  await page.goto(`/compare?a=${root.id}&b=${child.id}`);
  await expect(page.getByTestId("relation")).toHaveText("siblings", { timeout: 15_000 });
  const row = page.getByTestId("deltas").locator("tr", { hasText: "Integrated loudness" });
  await expect(row).toHaveCount(1);
  await expect(row.locator("td").nth(1)).toContainText(/-?\d+(\.\d+)?\s*LU/);
  await expect(page.getByRole("heading", { name: "What this cannot hear" })).toBeVisible();
});
