import { expect, test, type APIRequestContext, type Page } from "./fixtures";

async function trackId(request: APIRequestContext, title: string): Promise<string> {
  const r = await request.get("/api/tracks?limit=50");
  const items = ((await r.json()) as { items: { id: string; title: string }[] }).items;
  const t = items.find((x) => x.title === title);
  if (!t) throw new Error(`seed track ${title} missing`);
  return t.id;
}

const now = (page: Page) => page.evaluate(() => document.querySelector("audio")?.currentTime ?? -1);

test("deep link ?t=3 starts at 3 s once played", async ({ page, request }) => {
  const id = await trackId(request, "Syn song 03");
  await page.goto(`/tracks/${id}?t=3`);
  await expect(page.getByTestId("track-title")).toHaveText("Syn song 03");
  await expect(page.getByTestId("track-play")).toHaveText("Play"); // loaded paused
  await page.getByTestId("track-play").click();
  await expect(page.getByTestId("track-play")).toHaveText("Pause");
  await expect.poll(() => now(page), { timeout: 5000 }).toBeGreaterThanOrEqual(3);
  // the param is dropped so history never re-applies it
  await expect(page).toHaveURL(new RegExp(`/tracks/${id}$`));
});

test("timed track highlights its current section", async ({ page, request }) => {
  const id = await trackId(request, "Syn song 03");
  await page.goto(`/tracks/${id}`);
  await expect(page.getByTestId("lyrics-section")).toHaveCount(3);
  await expect(page.getByTestId("untimed-note")).toHaveCount(0);
  await page.getByTestId("track-play").click();
  await expect.poll(() => now(page), { timeout: 5000 }).toBeGreaterThanOrEqual(1);
  const active = page.locator('[data-testid="lyrics-section"][data-active="true"]');
  await expect(active).toHaveCount(1);
  await expect(active).toContainText(/verse/i);
  await page.getByTestId("lyrics").getByRole("button", { name: /chorus/i }).click();
  await expect.poll(() => now(page), { timeout: 5000 }).toBeGreaterThanOrEqual(4);
  await expect(active).toContainText(/chorus/i);
});

test("untimed track shows no highlight and literal markup", async ({ page, request }) => {
  const id = await trackId(request, "Syn song 04");
  await page.goto(`/tracks/${id}`);
  await expect(page.getByTestId("untimed-note")).toBeVisible();
  await page.getByTestId("track-play").click();
  await expect.poll(() => now(page), { timeout: 5000 }).toBeGreaterThanOrEqual(1);
  await expect(page.locator('[data-testid="lyrics-section"][data-active="true"]')).toHaveCount(0);
  const lyrics = page.getByTestId("lyrics");
  await expect(lyrics).toContainText("<b>&");
  await expect(lyrics.locator("b")).toHaveCount(0);
});
