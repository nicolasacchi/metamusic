import { expect, test, type APIRequestContext, type Page } from "./fixtures";

type Item = { id: string; title: string; duration_s: number };

async function tracks(request: APIRequestContext): Promise<Item[]> {
  const r = await request.get("/api/tracks?limit=50");
  return ((await r.json()) as { items: Item[] }).items;
}

const audioState = (page: Page) =>
  page.evaluate(() => {
    const a = document.querySelector("audio");
    return { t: a?.currentTime ?? -1, d: a?.duration ?? NaN, paused: a?.paused ?? true, src: a?.currentSrc ?? "" };
  });

test("plays and seeks a FLAC master", async ({ page, request }) => {
  const t = (await tracks(request)).find((x) => x.title === "Syn song 03");
  await page.goto(`/tracks/${t?.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audioState(page)).src).toMatch(/\/media\/[0-9a-f]{64}\.flac\?exp=/);
  await expect.poll(async () => (await audioState(page)).t, { timeout: 10_000 }).toBeGreaterThanOrEqual(0.2);
  // seek forward: playback continues from there
  await page.evaluate(() => {
    const a = document.querySelector("audio");
    if (a) a.currentTime = 6;
  });
  await expect.poll(async () => (await audioState(page)).t, { timeout: 10_000 }).toBeGreaterThanOrEqual(6);
});

test("waveform click seeks", async ({ page, request }) => {
  const t = (await tracks(request)).find((x) => x.title === "Syn song 03");
  await page.goto(`/tracks/${t?.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audioState(page)).t, { timeout: 5000 }).toBeGreaterThan(0.2);
  await page.getByTestId("track-play").click(); // pause so the clock does not drift
  await expect(page.getByTestId("track-play")).toHaveText("Play");
  const wave = page.locator("article").getByTestId("waveform");
  await wave.scrollIntoViewIfNeeded();
  const box = await wave.boundingBox();
  if (!box) throw new Error("waveform has no box");
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  const { d } = await audioState(page);
  expect(d).toBeGreaterThan(0);
  await expect.poll(async () => Math.abs((await audioState(page)).t - d / 2)).toBeLessThanOrEqual(0.5);
});

test("media requests are signed and ranged", async ({ page, request }) => {
  const t = (await tracks(request)).find((x) => x.title === "Syn song 05");
  const statuses: number[] = [];
  const urls: string[] = [];
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/media/")) {
      statuses.push(r.status());
      urls.push(r.url());
    }
  });
  await page.goto(`/tracks/${t?.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audioState(page)).t, { timeout: 5000 }).toBeGreaterThan(0.3);
  await expect.poll(() => statuses).toContain(206);
  for (const u of urls) expect(u).toMatch(/\/media\/[0-9a-f]{64}\.(flac|mp3|jpg)\?exp=\d+&sig=/);
});

test("playback persists across navigation", async ({ page, request }) => {
  const t = (await tracks(request)).find((x) => x.title === "Syn song 03");
  await page.goto(`/tracks/${t?.id}`);
  const audio = page.locator("audio");
  const id = await audio.getAttribute("data-audio-id");
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audioState(page)).t, { timeout: 5000 }).toBeGreaterThan(0.2);

  await page.getByRole("link", { name: "Library" }).click();
  await expect(page).toHaveURL(/\/$/);
  await page.getByRole("link", { name: "Backlog" }).click();
  await expect(page).toHaveURL(/\/backlog$/);
  const a = await audioState(page);
  await page.getByTestId("player-bar").getByRole("link", { name: /Syn song 03/ }).click();
  await expect(page).toHaveURL(new RegExp(`/tracks/${t?.id}$`));

  await expect(audio).toHaveAttribute("data-audio-id", id ?? "");
  const b = await audioState(page);
  expect(b.paused).toBe(false);
  expect(b.t).toBeGreaterThan(a.t);
  await expect.poll(async () => (await audioState(page)).t, { timeout: 4000 }).toBeGreaterThan(b.t);
});

// Continuous playback across a track boundary, in real time (the one such spec: the others seek, see
// m3-autoadvance.spec.ts). "Syn song 02" is the 2 s seed take, so the boundary costs about two seconds.
// Where @realtime specs run is decided by the e2e matrix rules (web/e2e/matrix.ts).
test("@realtime auto-advances to the next queue track without a gap", async ({ page, request }) => {
  const list = await tracks(request);
  const i = list.findIndex((x) => x.title === "Syn song 02");
  const next = list[i + 1];
  if (!next) throw new Error("no track after Syn song 02");
  await page.goto("/");
  await page.getByRole("button", { name: "Play Syn song 02" }).click();
  await expect.poll(async () => (await audioState(page)).t, { timeout: 5000 }).toBeGreaterThan(0.1);
  // no seek: the take ends by itself
  await expect(page.getByTestId("player-bar").getByRole("link", { name: next.title })).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause");
  await expect.poll(async () => (await audioState(page)).paused).toBe(false);
  await expect.poll(async () => (await audioState(page)).t, { timeout: 5000 }).toBeGreaterThan(0.3);
});
