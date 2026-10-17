// matrix: @mobile because the app shell (top bar, tab bar, mini-player) is the phone layout and must not cover or lose any page
import { expect, test, type Page } from "./fixtures";
import { NAV_LINKS } from "../src/lib/nav";
import { openNav, ownTracks, spaGoto } from "./helpers";
import { createDuel, siblings } from "./m2bApi";

const isPhone = (page: Page) => (page.viewportSize()?.width ?? 1280) < 768;

test("phone: the tab bar replaces the link wall and the header stays within 52 px", async ({ page }) => {
  test.skip(!isPhone(page), "phone layout");
  await page.goto("/");
  const tabs = page.getByRole("navigation", { name: "Main" });
  await expect(tabs).toBeVisible();
  await expect(tabs.getByRole("link")).toHaveText(["New", "Library", "Play queue", "Judge", "More"]);
  await expect(tabs.getByRole("link", { name: "Library" })).toHaveAttribute("aria-current", "page");
  const header = await page.getByTestId("topbar").boundingBox();
  expect(header?.height ?? 999).toBeLessThanOrEqual(52);
  // the bar sits at the bottom edge, its targets are at least 44 px, and nothing else is a main-nav link
  const vh = page.viewportSize()?.height ?? 0;
  const bar = await tabs.boundingBox();
  expect((bar?.y ?? 0) + (bar?.height ?? 0)).toBeGreaterThanOrEqual(vh - 1);
  for (const a of await tabs.getByRole("link").all()) expect((await a.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(44);
  expect(await page.locator("header a").count()).toBe(5);
});

test("desktop: one top-nav row of the same five groups", async ({ page }) => {
  test.skip(isPhone(page), "desktop layout");
  await page.goto("/");
  const tabs = page.getByRole("navigation", { name: "Main" });
  await expect(tabs.getByRole("link")).toHaveText(["New", "Library", "Play queue", "Judge", "More"]);
  const ys = await Promise.all((await tabs.getByRole("link").all()).map(async (a) => Math.round((await a.boundingBox())?.y ?? -1)));
  expect(new Set(ys).size, "all five on one row").toBe(1);
});

test("every route is reachable within two taps from its tab", async ({ page }) => {
  for (const l of NAV_LINKS) {
    if (l.path === "/offline") {
      await page.goto("/more");
      if ((await page.getByTestId("nav-offline").count()) === 0) continue; // the offline store is not available here
    }
    await page.goto("/new");
    await openNav(page, l.label); // at most two clicks: the tab, then the link on the page it opens
    const want = l.path.replace(/\/$/, "") || "/";
    await expect.poll(() => new URL(page.url()).pathname.replace(/\/$/, "") || "/", { message: l.label }).toBe(want);
  }
  // the three tabs that are pages of their own
  for (const [label, path] of [["New", "/new"], ["Play queue", "/queue"], ["More", "/more"]] as const) {
    await openNav(page, label);
    await expect(page).toHaveURL(new RegExp(`${path}$`));
  }
});

test("the phone footer stays within 64 px while playing and sits above the tab bar", async ({ page, request }, info) => {
  test.skip(!isPhone(page), "phone layout");
  const [t] = await ownTracks(request, info);
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause");
  const footer = await page.getByTestId("player-footer").boundingBox();
  const bar = await page.getByRole("navigation", { name: "Main" }).boundingBox();
  expect(footer?.height ?? 999).toBeLessThanOrEqual(64);
  expect((footer?.y ?? 0) + (footer?.height ?? 0)).toBeLessThanOrEqual((bar?.y ?? 0) + 1);
  // the mini-player body opens the queue, which lists the track playing
  await page.getByTestId("player-bar").getByRole("link").click();
  await expect(page).toHaveURL(/\/queue$/);
  await expect(page.getByTestId("queue-now")).toBeVisible();
  // the last row of a long page can be scrolled clear of both bars (client navigation keeps the player)
  await openNav(page, "More");
  await page.getByTestId("more-about").scrollIntoViewIfNeeded();
  const about = await page.getByTestId("more-about").boundingBox();
  const f2 = await page.getByTestId("player-footer").boundingBox();
  const t2 = await page.getByRole("navigation", { name: "Main" }).boundingBox();
  const top = Math.min(f2?.y ?? 1e9, t2?.y ?? 1e9);
  expect((about?.y ?? 0) + (about?.height ?? 0)).toBeLessThanOrEqual(top + 1);
});

test("duel verdict buttons are not covered by the player or the tab bar; the mini-player is blind", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  const { root, child } = await siblings(request);
  const duel = await createDuel(request, root.id, child.id);
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause");
  await spaGoto(page, `/duels/${duel.id}`);
  await expect(page.getByTestId("ab-play")).toBeVisible({ timeout: 20_000 });
  if (isPhone(page)) {
    await expect(page.getByTestId("player-bar")).toHaveText("Paused for duel");
    await expect(page.getByTestId("player-bar")).not.toContainText(t.title);
  }
  for (const id of ["verdict-a", "verdict-b", "verdict-tie", "verdict-both_bad", "verdict-submit"]) {
    const b = page.getByTestId(id);
    await b.scrollIntoViewIfNeeded();
    const covered = await b.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
      return !(hit === el || el.contains(hit));
    });
    expect(covered, `${id} is covered`).toBe(false);
  }
});
