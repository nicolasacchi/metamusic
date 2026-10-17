// matrix: @mobile @firefox because the shell, nav, 404 and narrow-viewport nav are cheap and are what every device loads first
import { expect, test } from "./fixtures";
import { openNav } from "./helpers";

test("More shows identity and version", async ({ page }) => {
  await page.goto("/more");
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
  await openNav(page, "Settings");
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByTestId("system-info")).toContainText("proxy");
  await expect(audio).toHaveAttribute("data-e2e-mark", "kept");
  await openNav(page, "More");
  await expect(page).toHaveURL(/\/more$/);
  await expect(page.getByTestId("identity")).toHaveText("e2e");
  await expect(audio).toHaveAttribute("data-e2e-mark", "kept");
});

// CI-only failure (old link wall): with a wider system font the header overflowed on phones and the nav links were
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
          return r.right > window.innerWidth || r.left < 0 || !(hit === a || a.contains(hit));
        })
        .map((a) => a.textContent),
    };
  });
  expect(report).toEqual({ overflowX: 0, covered: [] });
  for (const name of ["Backlog", "Import", "Settings", "Library"]) {
    await openNav(page, name);
  }
});

test("unknown route shows 404", async ({ page }) => {
  await page.goto("/no/such/page");
  await expect(page.getByRole("heading", { name: "Not found" })).toBeVisible();
});

test("dark color-scheme: native controls render on dark backgrounds", async ({ page }) => {
  await page.goto("/settings");
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).colorScheme)).toBe("dark");
  const lum = (rgb: string) => {
    const n = (rgb.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number);
    return (0.2126 * n[0]! + 0.7152 * n[1]! + 0.0722 * n[2]!) / 255;
  };
  const bg = await page.evaluate(() => {
    const mk = (tag: string) => {
      const el = document.body.appendChild(document.createElement(tag));
      const c = getComputedStyle(el).backgroundColor;
      const col = getComputedStyle(el).color;
      el.remove();
      return { c, col };
    };
    return { select: mk("select"), button: mk("button"), body: getComputedStyle(document.body).backgroundColor };
  });
  expect(lum(bg.body)).toBeLessThan(0.2);
  // a native select/button must not paint a light slab: transparent or dark background, light text
  for (const k of ["select", "button"] as const) {
    const { c, col } = bg[k];
    if (c !== "rgba(0, 0, 0, 0)") expect(lum(c)).toBeLessThan(0.3);
    expect(lum(col)).toBeGreaterThan(0.5);
  }
});
