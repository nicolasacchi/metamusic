// matrix: @mobile because the cover, download, offline and prompt actions of the track page are used from the phone
import { expect, test, type Page } from "./fixtures";
import { ownTracks } from "./helpers";

// Runs with the service worker allowed (the page is controlled after the first load); every project has its own server,
// so the offline set is its own. Clipboard access is granted to the context.
test.use({ serviceWorkers: "allow", permissions: ["clipboard-read", "clipboard-write"] });

test.describe.configure({ mode: "serial" });

const controlled = (page: Page) => page.evaluate(() => navigator.serviceWorker.controller !== null);

test("the page is controlled by the worker after the first load", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  await page.goto(`/tracks/${t.id}`);
  await expect.poll(() => controlled(page), { timeout: 15_000 }).toBe(true);
});

test("cover art loads and feeds the Media Session artwork", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  const cover = page.waitForResponse((r) => /^\/media\/[0-9a-f]{64}\.png$/.test(new URL(r.url()).pathname));
  await page.goto(`/tracks/${t.id}`);
  const r = await cover;
  expect(r.status()).toBe(200);
  expect(r.headers()["content-type"]).toContain("image/png");
  await expect(page.getByTestId("cover-art")).toBeVisible();
  await expect.poll(() => page.getByTestId("cover-art").evaluate((i: HTMLImageElement) => i.naturalWidth)).toBeGreaterThan(0);

  await page.getByTestId("track-play").click();
  await expect
    .poll(
      () =>
        page.evaluate(() =>
          (navigator.mediaSession.metadata?.artwork ?? []).map((a) => `${a.sizes}|${new URL(a.src, location.href).pathname}`),
        ),
      { timeout: 10_000 },
    )
    .toEqual([expect.stringMatching(/^512x512\|\/media\/[0-9a-f]{64}\.png$/), expect.stringMatching(/^1024x1024\|\/media\/[0-9a-f]{64}\.png$/)]);
});

test("Download FLAC is an attachment", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  await page.goto(`/tracks/${t.id}`);
  const link = page.getByTestId("download-flac");
  await expect(link).toBeVisible();
  const dl = page.waitForEvent("download");
  await link.click();
  expect((await dl).suggestedFilename()).toMatch(/\.flac$/);
});

test("Copy critique prompt puts the prompt on the clipboard", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("copy-critique-prompt").click();
  await expect(page.getByTestId("prompt-chars")).toContainText("copied");
  const text = await page.evaluate(() => navigator.clipboard.readText());
  expect(text.toLowerCase()).toContain("you cannot hear audio");
  expect(text).toContain(t.id);
});

test("Download for offline lists the track in /offline; Remove takes it away", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("offline-add").click();
  await expect(page.getByTestId("offline-state")).toHaveText("Downloaded", { timeout: 30_000 });

  await page.goto("/offline");
  const row = page.getByTestId("offline-row").filter({ hasText: t.title });
  await expect(row).toHaveAttribute("data-status", "complete");

  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("offline-remove").click();
  await expect(page.getByTestId("offline-add")).toBeVisible({ timeout: 15_000 });

  await page.goto("/offline");
  await expect(page.getByTestId("offline-list").or(page.getByTestId("offline-empty"))).toBeVisible();
  await expect(page.getByTestId("offline-row").filter({ hasText: t.title })).toHaveCount(0);
});
