// matrix: @mobile because Judge one by one is a phone flow: 52 px stars on a card, auto-advance, the card as the player, the Undo toast and the lock-screen metadata
// Wave 4b: the Judge tab. Rate takes one by one without leaving the page, Undo, and blind listening.
import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON } from "./helpers";

interface Row {
  id: string;
  title: string;
  stars: number | null;
  song_title: string | null;
  song_angle: string | null;
}

const unrated = async (request: APIRequestContext): Promise<Row[]> =>
  (await apiJSON<{ items: Row[] }>(request, "GET", "/api/tracks?unrated=true&sort=recorded_desc&limit=50")).items;

const stars = async (request: APIRequestContext, id: string): Promise<number | null> => {
  const d = await apiJSON<{ rating: { human: { stars: number | null } | null } }>(request, "GET", `/api/tracks/${id}`);
  return d.rating.human?.stars ?? null;
};

const card = (page: Page) => page.getByTestId("judge-card");
const isPhone = (page: Page) => (page.viewportSize()?.width ?? 1280) < 768;

test("rate five new takes in a row with undo, without leaving the page", async ({ page, request }) => {
  const todo = (await unrated(request)).slice(0, 5);
  expect(todo.length, "the e2e profile has five unrated takes").toBe(5);

  await page.goto("/judge");
  await page.evaluate(() => ((window as unknown as { __stay: number }).__stay = 1)); // a reload or full navigation drops this
  const url = page.url();
  await expect(card(page)).toHaveAttribute("data-track-id", todo[0]!.id);
  await expect(page.getByTestId("judge-progress")).toContainText(`1 of `);
  // the card is the player: the mini-player is gone
  await expect(page.getByTestId("player-bar")).toHaveCount(0);
  await expect(page.getByRole("navigation", { name: "Judge views" })).toBeVisible();
  // Song / angle on the card, from the search_tracks summary
  const t0 = todo[0]!;
  if (t0.song_title) await expect(page.getByTestId("judge-song-line")).toContainText(t0.song_title);
  // 52 px stars on touch
  if (isPhone(page)) expect((await card(page).getByRole("button", { name: "3 stars" }).boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(52);

  for (const [i, t] of todo.entries()) {
    await expect(card(page)).toHaveAttribute("data-track-id", t.id);
    await card(page).getByRole("button", { name: "4 stars", exact: true }).click();
    await expect(page.getByTestId("toast").filter({ hasText: "Rated 4 stars" })).toBeVisible();
    // auto-advance after 700 ms
    if (i < todo.length - 1) await expect(card(page)).toHaveAttribute("data-track-id", todo[i + 1]!.id);
    else await expect(page.getByTestId("judge-card").or(page.getByTestId("judge-done"))).not.toHaveAttribute("data-track-id", t.id);
    await expect.poll(() => stars(request, t.id)).toBe(4);
  }
  expect(page.url()).toBe(url);
  expect(await page.evaluate(() => (window as unknown as { __stay?: number }).__stay)).toBe(1);

  // Undo restores the previous rating through rate_track and returns to that take
  await page.getByTestId("toast").filter({ hasText: "Rated 4 stars" }).getByTestId("toast-action").click();
  await expect(card(page)).toHaveAttribute("data-track-id", todo[4]!.id);
  await expect.poll(() => stars(request, todo[4]!.id)).toBe(null);
  for (const t of todo.slice(0, 4)) expect(await stars(request, t.id)).toBe(4);
});

test("Undo inside the 700 ms window cancels the advance", async ({ page, request }) => {
  const first = (await unrated(request))[0]!;
  await page.goto("/judge");
  await expect(card(page)).toHaveAttribute("data-track-id", first.id);
  await card(page).getByRole("button", { name: "2 stars", exact: true }).click();
  await page.getByTestId("toast-action").click();
  await expect(card(page)).toHaveAttribute("data-track-id", first.id);
  await page.waitForTimeout(1000);
  await expect(card(page)).toHaveAttribute("data-track-id", first.id); // it did not advance behind the Undo
  await expect.poll(() => stars(request, first.id)).toBe(null);
});

test("Skip moves on without rating; the New inbox links in", async ({ page, request }) => {
  const [a, b] = await unrated(request);
  await page.goto("/judge");
  await expect(card(page)).toHaveAttribute("data-track-id", a!.id);
  await page.getByTestId("judge-skip").click();
  await expect(card(page)).toHaveAttribute("data-track-id", b!.id);
  expect(await stars(request, a!.id)).toBe(null);

  // from the New inbox (only when it holds new takes)
  await page.goto("/new");
  const link = page.getByTestId("judge-new");
  if ((await link.count()) > 0) {
    await link.click();
    await expect(page).toHaveURL(/\/judge\?from=new$/);
    await expect(page.getByTestId("judge-progress")).toContainText("Judge new");
  }
});

test("blind triage hides identity from the DOM and the Media Session", async ({ page, request }) => {
  const first = (await unrated(request))[0]!;
  await page.goto("/judge");
  await expect(card(page)).toHaveAttribute("data-track-id", first.id);
  await expect(card(page).getByTestId("judge-title")).toHaveText(first.title);

  await page.getByTestId("judge-blind").click();
  await expect(page.getByTestId("judge-blind")).toHaveAttribute("aria-pressed", "true");
  await expect(card(page).getByTestId("judge-title")).toHaveText("Take A");
  await expect(card(page).getByTestId("cover-art")).toHaveCount(0);
  await expect(card(page).getByTestId("cover-placeholder")).toHaveCount(0);
  await expect(card(page).getByTestId("waveform")).toHaveCount(0);
  await expect(card(page).getByRole("link", { name: "Open take" })).toHaveCount(0);

  // play it: the footer player is gone, so the only title that could leak is the lock screen
  await page.getByTestId("judge-play").click();
  await expect.poll(() => page.evaluate(() => document.querySelector("audio")?.currentTime ?? -1), { timeout: 10_000 }).toBeGreaterThan(0.2);
  await expect.poll(() => page.evaluate(() => navigator.mediaSession.metadata?.title ?? "")).toBe("Take A");
  const ms = await page.evaluate(() => {
    const m = navigator.mediaSession.metadata;
    return { title: m?.title ?? "", album: m?.album ?? "", art: (m?.artwork ?? []).map((a) => a.src) };
  });
  expect(ms.album).toBe("");
  for (const art of ms.art) expect(art).not.toContain(first.id);
  expect(JSON.stringify(ms)).not.toContain(first.title);

  // nothing of the identity anywhere in the page
  const html = await page.content();
  expect(html).not.toContain(first.title);
  if (first.song_title) expect(html).not.toContain(first.song_title);
  if (first.song_angle) expect(html).not.toContain(first.song_angle);
  // a rating toast names no take either
  await card(page).getByRole("button", { name: "5 stars", exact: true }).click();
  await expect(page.getByTestId("toast").filter({ hasText: "Rated 5 stars" })).toBeVisible();
  expect(await page.content()).not.toContain(first.title);
  await page.getByTestId("toast-action").click(); // leave the library as it was
  await expect.poll(() => stars(request, first.id)).toBe(null);

  // blind off: the real metadata returns
  await page.getByTestId("judge-blind").click();
  await expect(card(page).getByTestId("judge-title")).toHaveText(first.title);
  await expect.poll(() => page.evaluate(() => navigator.mediaSession.metadata?.title ?? "")).toBe(first.title);
});
