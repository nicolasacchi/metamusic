// matrix: @mobile @firefox because the premature-end bug is Firefox audio output behaviour; phones and Chrome run the same player code and prove the guard does not disturb their normal end
// A song that stops mid-way and skips to the next one ("on library the song never finishes"). Operator telemetry: an
// `error` event, playback bookkeeping continues normally for 10-60 s, then `complete` + `advance` far before the end.
// Every one of those errors left the position untouched (no source swap), which only the Firefox audio-sink path
// does: the player ignores that error (the media clock keeps running) and then believed the stream's early `ended`.
// Headless browsers have no audio output to break, so the test injects what Firefox does: a MediaSink DECODE error on
// the element, then an `ended` the media clock could not have reached yet.
import { expect, test, type APIRequestContext, type Page } from "./fixtures";

type Item = { id: string; title: string };

async function tracks(request: APIRequestContext): Promise<Item[]> {
  const r = await request.get("/api/tracks?limit=50");
  return ((await r.json()) as { items: Item[] }).items;
}

test.describe.configure({ timeout: 90_000 });

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { __fin: number; __ended: number };
    w.__fin = 0;
    w.__ended = 0;
    // The listen recorder's `complete` follows the player's `mm:finish` (and a native `ended` it hears itself).
    document.addEventListener("mm:finish", () => w.__fin++, true);
    document.addEventListener("ended", () => w.__ended++, true);
  });
});

const audio = (page: Page) =>
  page.evaluate(() => {
    const a = document.querySelector("audio");
    return { t: a?.currentTime ?? -1, d: a?.duration ?? NaN, paused: a?.paused ?? true };
  });

const finishes = (page: Page) => page.evaluate(() => (window as unknown as { __fin: number }).__fin);

test("an `ended` after a Firefox audio-output error does not finish the song or skip the queue", async ({ page, request }) => {
  const list = await tracks(request);
  const first = list.find((x) => x.title === "Syn song 03"); // the 12 s FLAC take
  if (!first) throw new Error("seed track missing");
  const at = list.indexOf(first);
  const next = list[at + 1];
  if (!next) throw new Error("seed queue has no track after the 12 s take");
  const bar = page.getByTestId("player-bar");
  await page.goto("/library");
  await page.getByRole("button", { name: `Play ${first.title}`, exact: true }).click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 8000 }).toBeGreaterThan(1.5);
  await expect.poll(async () => (await audio(page)).d).toBeGreaterThan(10);

  // Firefox: the output failed; the clock keeps running, the element reports MEDIA_ERR_DECODE "OnMediaSinkAudioError".
  const errAt = await page.evaluate(() => {
    const a = document.querySelector("audio") as HTMLAudioElement;
    Object.defineProperty(a, "error", { configurable: true, get: () => ({ code: 3, message: "OnMediaSinkAudioError" }) });
    a.dispatchEvent(new Event("error"));
    // a real element drops the error when it loads a new source
    a.addEventListener("loadedmetadata", () => Reflect.deleteProperty(a, "error"), { once: true });
    return a.currentTime;
  });
  await page.waitForTimeout(500);
  // ... and then the stream gives up long before its end: a genuine `ended` event at 2 s of clock after the error.
  await page.evaluate(() => {
    const a = document.querySelector("audio") as HTMLAudioElement;
    a.currentTime = a.duration;
  });

  // The song must not finish: still the same track, no finish seen, the position goes on from the error point.
  await page.waitForTimeout(1500);
  await expect(bar.getByRole("link", { name: first.title })).toBeVisible();
  expect(await finishes(page)).toBe(0);
  await expect.poll(async () => (await audio(page)).t, { timeout: 8000 }).toBeGreaterThan(errAt - 0.5);
  const mid = await audio(page);
  expect(mid.d - mid.t).toBeGreaterThan(2); // really mid-song, not parked at the end
  expect(mid.paused).toBe(false);

  // It still completes at its real end, once, and the queue moves on then.
  await expect(bar.getByRole("link", { name: next.title })).toBeVisible({ timeout: 30_000 });
  expect(await finishes(page)).toBe(1);
});
