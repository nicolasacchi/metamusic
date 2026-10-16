// matrix: @mobile @firefox because the shell, nav, 404 and narrow-viewport nav are cheap and are what every device loads first
import { expect, test } from "./fixtures";

test("home shows identity and version", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByTestId("identity")).toHaveText("e2e");
  await expect(page.getByTestId("version")).toHaveText(/^ok \S+/);
});

test("settings renders system info", async ({ page }) => {
  await page.goto("/settings");
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
  await expect(page.getByTestId("system-info")).toContainText("proxy");
});

test("navigation keeps the same audio element", async ({ page }) => {
  await page.goto("/");
  const audio = page.locator("audio");
  await audio.evaluate((el) => {
    (el as HTMLElement).dataset.e2eMark = "kept";
  });
  await page.getByRole("link", { name: "Settings" }).click();
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByTestId("system-info")).toContainText("proxy");
  await expect(audio).toHaveAttribute("data-e2e-mark", "kept");
  await page.getByRole("link", { name: "Library" }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByTestId("identity")).toHaveText("e2e");
  await expect(audio).toHaveAttribute("data-e2e-mark", "kept");
});

// CI-only failure: with a wider system font the header overflowed on phones and the nav links were
// covered by each other / the header. Force a wide font at a narrow viewport so this is font-independent.
test("main nav stays reachable at a narrow viewport with a wide font", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 });
  await page.goto("/");
  await page.addStyleTag({ content: "* { font-family: 'DejaVu Sans', 'Courier New', monospace !important; }" });
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  const report = await page.evaluate(() => {
    const links = [...document.querySelectorAll<HTMLAnchorElement>('nav[aria-label="Main"] a')];
    return {
      overflowX: document.documentElement.scrollWidth - window.innerWidth,
      covered: links
        .filter((a) => {
          const r = a.getBoundingClientRect();
          const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
          return r.right > window.innerWidth || hit !== a;
        })
        .map((a) => a.textContent),
    };
  });
  expect(report).toEqual({ overflowX: 0, covered: [] });
  for (const name of ["Backlog", "Import", "Settings", "Library"]) {
    await page.getByRole("link", { name }).click({ timeout: 5000 });
  }
});

test("unknown route shows 404", async ({ page }) => {
  await page.goto("/no/such/page");
  await expect(page.getByRole("heading", { name: "Not found" })).toBeVisible();
});
