// matrix: chrome-only because it changes the caps and arming for everyone and runs alone
import { expect, test } from "./fixtures";
import { apiJSON } from "./helpers";
import { composeRoots, first } from "./m3Api";

// The serial last project (playwright.config.ts): it changes the caps and the arming for everyone, so it runs when no
// other M3 spec (and no other project) is left. Both changes are undone in `finally`.

test.describe.configure({ mode: "serial", timeout: 150_000 });

interface Budget {
  caps: { monthly_micro_usd: number };
}

test("a monthly cap below the estimate disables Confirm; raising it enables it again", async ({ page, request }) => {
  const mother = first(await composeRoots(request, 1, "m3s"));
  const before = (await apiJSON<Budget>(request, "GET", "/api/budget")).caps.monthly_micro_usd;
  const cap = page.getByTestId("cap-monthly_micro_usd");
  try {
    await page.goto("/human/spend");
    await expect(cap).not.toHaveValue("");
    const original = await cap.inputValue();
    await cap.fill("0.001");
    await page.getByTestId("save-caps").click();
    await expect.poll(async () => (await apiJSON<Budget>(request, "GET", "/api/budget")).caps.monthly_micro_usd).toBe(1000);

    await page.goto(`/tracks/${mother}`);
    await page.getByTestId("generate-button").locator("summary").click();
    await page.getByTestId("variant-new_seed").click();
    await expect(page.getByTestId("estimate")).toBeVisible();
    await expect(page.getByTestId("disabled-reasons")).toContainText(/monthly cap would be exceeded/);
    await expect(page.getByTestId("confirm-button")).toBeDisabled();

    await page.goto("/human/spend");
    await expect(cap).toHaveValue("0.001");
    await cap.fill(original);
    await page.getByTestId("save-caps").click();
    await expect.poll(async () => (await apiJSON<Budget>(request, "GET", "/api/budget")).caps.monthly_micro_usd).toBe(before);

    // the intent survives in the tab: the page prices it again, now within the cap
    await page.goto("/human/generate");
    await expect(page.getByTestId("estimate")).toBeVisible();
    await expect(page.getByTestId("disabled-reasons")).toHaveCount(0);
    await expect(page.getByTestId("confirm-button")).toBeEnabled();
  } finally {
    await apiJSON(request, "PUT", "/human/api/spend/caps", { monthly_micro_usd: before });
  }
});

test("disarming shows the banner; arming clears it", async ({ page, request }) => {
  try {
    await page.goto("/human/spend");
    await expect(page.getByTestId("armed-state")).toHaveText("armed");
    await expect(page.getByTestId("provider-banner")).toHaveCount(0);
    await page.getByTestId("provider-reason").fill("e2e disarm check");
    await page.getByTestId("disarm").click();
    await expect(page.getByTestId("armed-state")).toHaveText("disarmed");
    await page.reload();
    await expect(page.getByTestId("provider-banner")).toContainText("disarmed");

    await page.getByTestId("provider-reason").fill("e2e arm check");
    await page.getByTestId("arm").click();
    await expect(page.getByTestId("armed-state")).toHaveText("armed");
    await page.reload();
    await expect(page.getByTestId("armed-state")).toHaveText("armed");
    await expect(page.getByTestId("provider-banner")).toHaveCount(0);
  } finally {
    await apiJSON(request, "POST", "/human/api/provider/state", { armed: true, reason: "e2e restore" });
  }
});
