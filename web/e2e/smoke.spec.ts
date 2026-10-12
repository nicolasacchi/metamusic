import { expect, test } from "@playwright/test";

test("home shows identity and version", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByTestId("identity")).toHaveText("e2e");
  await expect(page.getByTestId("version")).toHaveText(/^ok \S+/);
});

test("settings renders system info", async ({ page }) => {
  await page.goto("/settings");
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
});

test("navigation keeps the same audio element", async ({ page }) => {
  await page.goto("/");
  const audio = page.locator("audio");
  const id = await audio.getAttribute("data-audio-id");
  expect(id).toBeTruthy();
  await page.getByRole("link", { name: "Settings" }).click();
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
  await expect(audio).toHaveAttribute("data-audio-id", id!);
  await page.getByRole("link", { name: "Library" }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByTestId("identity")).toHaveText("e2e");
  await expect(audio).toHaveAttribute("data-audio-id", id!);
});

test("unknown route shows 404", async ({ page }) => {
  await page.goto("/no/such/page");
  await expect(page.getByRole("heading", { name: "Not found" })).toBeVisible();
});
