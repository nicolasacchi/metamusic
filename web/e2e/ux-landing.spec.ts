// matrix: @mobile because the landing page and the tab bar are the phone entry point (a PWA cold start opens on New)
import { expect, test } from "./fixtures";
import { apiJSON } from "./helpers";
import { composeSong, getSong, type SongList } from "./l1Api";

test("the app opens on New", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByTestId("new-header")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: /^New\b/ })).toHaveAttribute("aria-current", "page");
  await expect(page.getByTestId("track-table")).toHaveCount(0);
});

test("an old Library bookmark at /?status=draft is replaced by /library?status=draft", async ({ page }) => {
  await page.goto("/");
  const entries = await page.evaluate(() => history.length);
  await page.goto("/?status=draft&min_stars=2");
  await expect(page).toHaveURL((u) => u.pathname === "/library" && u.search === "?status=draft&min_stars=2");
  await expect(page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: /^Library\b/ })).toHaveAttribute("aria-current", "page");
  // replaced, not pushed: the dead "/?..." entry is not left behind
  expect(await page.evaluate(() => history.length)).toBe(entries + 1);
});

test("the Library tab goes to /library", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: /^Library\b/ }).click();
  await expect(page).toHaveURL(/\/library$/);
  await expect(page.locator("[data-testid=track-table], [data-testid=track-list]")).toBeVisible();
});

test("the Song page's Judge button opens the one-by-one card with that Song's takes", async ({ page, request }) => {
  const list = await apiJSON<SongList>(request, "GET", "/api/songs?limit=50");
  const songId = list.items.find((s) => s.takes >= 1)?.id ?? (await composeSong(request, "ul", 2)).songId;
  const ids = new Set((await getSong(request, songId)).takes.map((t) => t.id));

  await page.goto(`/songs/${songId}`);
  await page.getByTestId("song-judge").click();
  await expect(page).toHaveURL(new RegExp(`/judge\\?song=${songId}$`));
  const card = page.getByTestId("judge-card");
  await expect(card).toBeVisible();
  expect(ids.has((await card.getAttribute("data-track-id")) ?? "")).toBe(true);
});
