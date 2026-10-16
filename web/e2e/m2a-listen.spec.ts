// matrix: @mobile @firefox @realtime because it checks play events (offline IndexedDB queue, beacon on leave) in every browser engine
import { expect, test, type APIRequestContext, type Page, type TestInfo } from "./fixtures";
import { allTracks, apiJSON, audioTime, tap, type Item } from "./helpers";

// One seed track per project (this spec runs alone in the listen-* projects, see playwright.config.ts): plays are counted per track and the projects run in parallel.
async function listenTrack(request: APIRequestContext, info: TestInfo): Promise<Item> {
  const all = (await allTracks(request)).filter((x) => x.title.startsWith("Syn song")).sort((x, y) => (x.title === y.title ? (x.id < y.id ? -1 : 1) : x.title < y.title ? -1 : 1));
  const i = ["chrome", "chrome-pixel7", "chrome-iphone15", "firefox"].indexOf(info.project.name.replace(/^listen-/, ""));
  const t = all[Math.max(0, i)];
  if (!t) throw new Error("seed track missing");
  return t;
}

const plays = async (request: APIRequestContext, id: string) =>
  (await apiJSON<{ listening: { plays: number } }>(request, "GET", `/api/tracks/${id}/context`)).listening.plays;

/** Number of events waiting in the SPA's IndexedDB outbox. */
const pending = (page: Page) =>
  page.evaluate(
    () =>
      new Promise<number>((resolve, reject) => {
        const open = indexedDB.open("mm-outbox", 1);
        open.onerror = () => reject(open.error);
        open.onsuccess = () => {
          const db = open.result;
          if (!db.objectStoreNames.contains("events")) return resolve(0);
          const r = db.transaction("events", "readonly").objectStore("events").count();
          r.onsuccess = () => resolve(r.result);
          r.onerror = () => reject(r.error);
        };
      }),
  );

// Plays are counted per track and each project owns one, so a project's tests run one after the other.
test.describe.configure({ mode: "serial" });

async function playFor(page: Page, seconds: number) {
  await page.getByTestId("track-play").click();
  await expect.poll(() => audioTime(page), { timeout: 10_000 }).toBeGreaterThan(seconds);
}

test("boot makes one device-probe request, not a loop", async ({ page }) => {
  let probes = 0;
  page.on("request", (r) => {
    if (new URL(r.url()).pathname === "/api/device-probe") probes++;
  });
  await page.goto("/");
  await expect(page.getByTestId("track-table")).toBeVisible();
  await page.waitForTimeout(1500);
  expect(probes).toBeLessThanOrEqual(2);
});

test("play, navigate away, pause: the plays count in the track context", async ({ page, request }, info) => {
  const t = await listenTrack(request, info);
  const before = await plays(request, t.id);
  await page.goto(`/tracks/${t.id}`);
  await playFor(page, 3);
  // client-side navigation keeps playing; the recorder follows the one audio element
  await page.getByRole("link", { name: "Library" }).click();
  await expect(page.getByTestId("track-table")).toBeVisible();
  await tap(page, page.getByTestId("play-toggle")); // pause from the player bar
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Play");
  // leaving the page flushes the outbox by beacon
  await page.goto("/settings");
  await expect.poll(() => plays(request, t.id), { timeout: 15_000 }).toBeGreaterThanOrEqual(before + 1);
});

test("offline: events stay in IndexedDB and are flushed when back online", async ({ page, request, context }, info) => {
  const t = await listenTrack(request, info);
  const before = await plays(request, t.id);
  await page.goto(`/tracks/${t.id}`);
  await playFor(page, 1.5);
  await context.setOffline(true);
  await page.waitForTimeout(800);
  await page.getByTestId("track-play").click(); // pause: closes the segment while offline
  await expect(page.getByTestId("track-play")).toHaveText("Play");
  await expect.poll(() => pending(page)).toBeGreaterThan(0);
  const held = await pending(page);
  await page.waitForTimeout(1000);
  expect(await pending(page)).toBeGreaterThanOrEqual(held);
  await context.setOffline(false);
  await expect.poll(() => pending(page), { timeout: 15_000 }).toBe(0);
  await expect.poll(() => plays(request, t.id), { timeout: 15_000 }).toBeGreaterThanOrEqual(before + 1);
});

test("leaving the page sends a beacon and the next load adds no duplicates", async ({ page, request, context }, info) => {
  const t = await listenTrack(request, info);
  const before = await plays(request, t.id);
  // Playwright reports no request a document sends while unloading, and the proxy identity headers this
  // suite injects do not reach it, so the call itself is recorded; the Go tests cover the server side of
  // `?mm_client=spa`. What the harness can show is that the SPA beacons the pending events on hide and
  // that the re-send on the next load is counted once.
  await context.addInitScript(() => {
    const send = navigator.sendBeacon.bind(navigator);
    navigator.sendBeacon = (url, data) => {
      const size = data instanceof Blob ? data.size : -1;
      localStorage.setItem("e2e-beacons", (localStorage.getItem("e2e-beacons") ?? "") + `${String(url)} ${size}\n`);
      return send(url, data);
    };
  });
  await page.goto(`/tracks/${t.id}`);
  await playFor(page, 3);
  await page.getByTestId("track-play").click();
  await expect(page.getByTestId("track-play")).toHaveText("Play");
  expect(await pending(page)).toBeGreaterThan(0);
  // The pause segment is appended to IndexedDB asynchronously. An unload beacon from this suite carries no
  // proxy identity, so a row still in flight when the document goes away would be lost; wait until the
  // outbox count has settled before leaving.
  let last = -1;
  await expect
    .poll(
      async () => {
        const n = await pending(page);
        const settled = n === last;
        last = n;
        return settled;
      },
      { intervals: [400], timeout: 10_000 },
    )
    .toBe(true);
  // leaving the document fires pagehide
  await page.goto("/healthz");
  const beacons = (await page.evaluate(() => localStorage.getItem("e2e-beacons"))) ?? "";
  expect(beacons).toMatch(/^\/api\/listen-events\?mm_client=spa [1-9]\d*$/m);

  // the beaconed rows stay in the outbox until the next load re-sends them; the server dedups by id
  const next = await context.newPage();
  await next.goto("/");
  await expect(next.getByTestId("track-table")).toBeVisible();
  await expect.poll(() => pending(next), { timeout: 15_000 }).toBe(0);
  await expect.poll(() => plays(request, t.id), { timeout: 15_000 }).toBe(before + 1);
  // a second load re-sends nothing and the count stays
  await next.reload();
  await expect(next.getByTestId("track-table")).toBeVisible();
  await next.waitForTimeout(500);
  expect(await plays(request, t.id)).toBe(before + 1);
});
