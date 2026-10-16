// matrix: @mobile because the operator manages connectors and Subsonic clients from the phone
import { expect, test } from "./fixtures";
import { uniq } from "./helpers";

// The connector grant seeded by `seed-synthetic --profile e2e` (client e2e-connector, label "E2E Connector").
// The spec restores everything it edits, so it is independent of order and of other specs.
test.describe.configure({ mode: "serial" });

const grantRow = (page: import("@playwright/test").Page) => page.getByTestId("grant-row").filter({ hasText: "E2E Connector" });

async function save(page: import("@playwright/test").Page) {
  const done = page.waitForResponse((r) => r.request().method() === "PUT" && new URL(r.url()).pathname.startsWith("/api/connectors/"));
  await grantRow(page).getByTestId("grant-save").click();
  expect((await done).ok()).toBe(true);
}

test("connectors: scopes, a daily budget, revoke and restore", async ({ page }) => {
  await page.goto("/human/connectors");
  const row = grantRow(page);
  await expect(row).toBeVisible();
  // seeded with read, write (read is always on and disabled)
  await expect(row.getByTestId("grant-write")).toBeChecked();
  await expect(row.getByTestId("grant-generate")).not.toBeChecked();

  try {
    await row.getByTestId("grant-generate").check();
    await row.getByTestId("grant-daily").fill("0.50");
    await save(page);

    // the row shows the new values after a reload
    await page.reload();
    await expect(grantRow(page).getByTestId("grant-generate")).toBeChecked();
    await expect(grantRow(page).getByTestId("grant-daily")).toHaveValue(/^0\.5(0)?$/);

    page.once("dialog", (d) => void d.accept()); // confirm("Revoke ...")
    await grantRow(page).getByTestId("grant-revoke").click();
    await expect(grantRow(page)).toContainText("revoked");
    await grantRow(page).getByTestId("grant-restore").click();
    await expect(grantRow(page)).toContainText("active");
  } finally {
    // restore the seeded grant: read, write; no daily budget; active
    await page.goto("/human/connectors");
    const r = grantRow(page);
    if (await r.getByTestId("grant-restore").count()) await r.getByTestId("grant-restore").click();
    await expect(grantRow(page).getByTestId("grant-revoke")).toBeVisible();
    await grantRow(page).getByTestId("grant-generate").uncheck();
    await grantRow(page).getByTestId("grant-daily").fill("");
    await save(page);
    await page.reload();
    await expect(grantRow(page).getByTestId("grant-generate")).not.toBeChecked();
    await expect(grantRow(page).getByTestId("grant-daily")).toHaveValue("");
    await expect(grantRow(page)).toContainText("active");
  }
});

test("subsonic clients: credentials are shown once, then only their presence", async ({ page }) => {
  const name = uniq("Test phone");
  await page.goto("/human/subsonic");
  await page.getByTestId("subsonic-name").fill(name);
  await page.getByTestId("subsonic-create").click();

  await expect(page.getByTestId("subsonic-reveal")).toBeVisible();
  const key = ((await page.getByTestId("subsonic-apikey").textContent()) ?? "").trim();
  const password = ((await page.getByTestId("subsonic-password").textContent()) ?? "").trim();
  expect(key).toMatch(/^mms_/);
  expect(password.length).toBeGreaterThan(8);

  // reload: the secrets are gone, the row says a key exists
  await page.reload();
  await expect(page.getByTestId("subsonic-reveal")).toHaveCount(0);
  const row = page.getByTestId("subsonic-list").locator("tr").filter({ hasText: name });
  await expect(row).toBeVisible();
  await expect(row).toContainText("API key");
  await expect(page.locator("body")).not.toContainText(key);
  await expect(page.locator("body")).not.toContainText(password);

  page.once("dialog", (d) => void d.accept()); // confirm("Revoke ...")
  await row.getByTestId("subsonic-revoke").click();
  await expect(row).toContainText("revoked");
});
