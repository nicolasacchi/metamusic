import { expect, test } from "./fixtures";

test("Back up now appears as a completed run in the list", async ({ page }) => {
  await page.goto("/settings/backups");
  const before = await page.getByTestId("backup-runs").locator("tbody tr").count().catch(() => 0);
  await page.getByTestId("backup-now").click();
  const runs = page.getByTestId("backup-runs");
  await expect(runs.locator('tbody tr[data-state="completed"]').first()).toBeVisible({ timeout: 30_000 });
  await expect.poll(() => runs.locator("tbody tr").count()).toBeGreaterThan(before);
  await expect(runs.locator("tbody tr").first()).toContainText("manual");
  await expect(page.getByTestId("backup-snapshots").locator("tbody tr").first()).toBeVisible();
  // the schedule is off in e2e (MM_BACKUP_AT=off)
  await expect(page.getByTestId("backup-schedule")).toContainText("Schedule: off");
});
