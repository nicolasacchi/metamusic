import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "./fixtures";

const SOURCE = "music/songs.jsonl";

function seedPath(dir: string): string {
  return join(dir, SOURCE);
}

async function chooseSource(page: Page, forBatch: boolean) {
  await page.goto("/export");
  await page.getByRole("radio", { name: "A source file" }).check();
  await expect(page.getByTestId("export-source").locator(`option[value="${SOURCE}"]`)).toHaveCount(1);
  await page.getByTestId("export-source").selectOption(SOURCE);
  if (forBatch) await page.getByTestId("export-for-batch").check();
  await page.getByTestId("export-submit").click();
  await expect(page.getByTestId("export-result")).toBeVisible();
}

test("source export is the original file byte for byte and reports exactly its own issues", async ({ page, seedDir }) => {
  await chooseSource(page, false);
  const card = page.getByTestId("validator-card");
  await expect(card).toHaveAttribute("data-tone", "original");
  await expect(card).toContainText("issues of the original file");
  // the e2e profile's two siblings share a request id: exactly one duplicate_id, nothing else
  await expect(card.locator("li")).toHaveCount(1);
  await expect(card.locator('li[data-code="duplicate_id"]')).toHaveCount(1);
  await expect(page.getByTestId("export-renamed")).toHaveCount(0);

  const download = page.waitForEvent("download");
  await page.getByTestId("export-download").click();
  const got = readFileSync(await (await download).path());
  const seed = readFileSync(seedPath(seedDir));
  expect(got.equals(seed)).toBe(true);
  // the shown digest is the digest of those bytes
  const { createHash } = await import("node:crypto");
  await expect(page.getByTestId("export-sha")).toHaveText(createHash("sha256").update(seed).digest("hex"));
});

test("for_batch renames ids and validates clean; a tracks export validates clean; the result reloads", async ({ page }) => {
  await chooseSource(page, true);
  await expect(page.getByTestId("validator-card")).toHaveAttribute("data-tone", "ok");
  await expect(page.getByTestId("export-renamed").locator("li").first()).toContainText("→");
  await expect(page).toHaveURL(/export=/);
  const sha = await page.getByTestId("export-sha").textContent();

  // reloading shows the same result through get_export
  await page.reload();
  await expect(page.getByTestId("export-result")).toBeVisible();
  await expect(page.getByTestId("export-sha")).toHaveText(sha ?? "");
  await expect(page.getByTestId("validator-card")).toHaveAttribute("data-tone", "ok");
  await expect(page.getByTestId("export-renamed").locator("li").first()).toContainText("→");

  await page.goto("/export");
  await page.getByRole("radio", { name: "Tracks" }).check();
  await page.getByTestId("export-submit").click();
  await expect(page.getByTestId("export-result")).toBeVisible();
  await expect(page.getByTestId("validator-card")).toHaveAttribute("data-tone", "ok");
  const tracksUrl = page.url();
  await page.reload();
  expect(page.url()).toBe(tracksUrl);
  await expect(page.getByTestId("validator-card")).toHaveAttribute("data-tone", "ok");
});
