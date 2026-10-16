// Auto-advance hardening (Android Firefox: at the end of a song the next one did not start).
// The "firefox-android" project runs this spec with an Android Firefox user agent, a phone viewport,
// touch and Firefox's strict autoplay prefs; chrome and the Pixel 7 project run the same specs.
//
// These specs test the player's logic (queue, refused play(), watchdog), not media timing, so they never play a
// track through: they start it, seek to 0.3 s before the end and await `ended` and the next track. The watchdog
// timers run on `page.clock` (it fakes timers and Date only, never media playback). The single real-time test
// here is tagged @realtime: it lets the 2 s seed take "Syn song 02" end by itself under strict autoplay. Where
// @realtime tests run (every project, a nightly job, a quarantine) is decided by the e2e matrix rules (web/e2e/matrix.ts).
import { expect, test, type APIRequestContext, type Page } from "./fixtures";

type Item = { id: string; title: string };

async function tracks(request: APIRequestContext): Promise<Item[]> {
  const r = await request.get("/api/tracks?limit=50");
  return ((await r.json()) as { items: Item[] }).items;
}

const audioState = (page: Page) =>
  page.evaluate(() => {
    const a = document.querySelector("audio");
    return { t: a?.currentTime ?? -1, d: a?.duration ?? NaN, paused: a?.paused ?? true, src: a?.currentSrc ?? "" };
  });

const sessionState = (page: Page) => page.evaluate(() => navigator.mediaSession.playbackState);

/**
 * Listen-event kinds the SPA queued or already sent. Events leave the outbox once delivered (every 10 events, on the 30 s
 * timer, on hide) and a request can be invisible to Playwright, so a single snapshot can lose events a previous one saw.
 * The result is therefore the union, by event id, of every request body and outbox snapshot seen so far: it only grows,
 * and an event counts once however many places it was seen in.
 */
function watchKinds(page: Page) {
  const seen = new Map<string, string>();
  page.on("request", (r) => {
    if (new URL(r.url()).pathname === "/api/listen-events" && r.postData()) {
      for (const e of (JSON.parse(r.postData() ?? "{}") as { events?: { id: string; kind: string }[] }).events ?? []) seen.set(e.id, e.kind);
    }
  });
  const queued = () =>
    page.evaluate(
      () =>
        new Promise<{ id: string; kind: string }[]>((resolve) => {
          const open = indexedDB.open("mm-outbox", 1);
          open.onerror = () => resolve([]);
          open.onsuccess = () => {
            const db = open.result;
            if (!db.objectStoreNames.contains("events")) return resolve([]);
            const r = db.transaction("events", "readonly").objectStore("events").getAll();
            r.onsuccess = () => resolve((r.result as { event: { id: string; kind: string } }[]).map((x) => x.event));
            r.onerror = () => resolve([]);
          };
        }),
    );
  return async () => {
    for (const e of await queued()) seen.set(e.id, e.kind);
    return [...seen.values()];
  };
}

// Root cause of the Android-Firefox flake: nothing in the player. Firefox's cold start (context creation, the first SPA
// load, the worker's server start) takes 10-60 s on a starved machine and counted against the default 30 s test timeout,
// so the first test of a worker died in startFirst before any player logic ran (the page snapshot of such a failure
// shows the finished Library). The tests themselves need about 5 s on an idle machine, hence a budget sized for the
// cold start rather than retries or sleeps.
test.describe.configure({ timeout: 90_000 });

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { __blockPlay: boolean; __noEnded: boolean };
    w.__blockPlay = false;
    w.__noEnded = false;
    // A play() that the autoplay policy refuses (Firefox Android after the activation window is gone).
    const orig = HTMLMediaElement.prototype.play;
    HTMLMediaElement.prototype.play = function (this: HTMLMediaElement) {
      if (w.__blockPlay) return Promise.reject(new DOMException("blocked by autoplay policy", "NotAllowedError"));
      return orig.call(this);
    };
    // A stream whose `ended` never reaches the page.
    document.addEventListener(
      "ended",
      (e) => {
        if (w.__noEnded) e.stopImmediatePropagation();
      },
      true,
    );
  });
});

async function startFirst(page: Page, list: Item[], index: number) {
  const t = list[index];
  if (!t) throw new Error("seed track missing");
  await page.goto("/");
  await page.getByRole("button", { name: `Play ${t.title}`, exact: true }).click();
  await expect.poll(async () => (await audioState(page)).t, { timeout: 8000 }).toBeGreaterThan(0.1);
  return t;
}

/** Seek to `before` seconds short of the end; the native `ended` (or the watchdog) follows within a fraction of a second. */
const toEnd = (page: Page, before = 0.3) =>
  page.evaluate((b) => {
    const a = document.querySelector("audio");
    if (a) a.currentTime = a.duration - b;
  }, before);

test("a refused play() after ended keeps the queue and resumes with the tap-to-continue button", async ({ page, request }) => {
  const list = await tracks(request);
  const kinds = watchKinds(page);
  await startFirst(page, list, 3);
  await page.evaluate(() => ((window as unknown as { __blockPlay: boolean }).__blockPlay = true));
  await toEnd(page);
  const bar = page.getByTestId("player-bar");
  await expect(bar.getByRole("link", { name: list[4]!.title })).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId("player-continue")).toBeVisible();
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Play");
  expect((await audioState(page)).paused).toBe(true);
  expect(await sessionState(page)).toBe("paused");
  await page.evaluate(() => ((window as unknown as { __blockPlay: boolean }).__blockPlay = false));
  await page.getByTestId("player-continue").click();
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause");
  await expect(page.getByTestId("player-continue")).toHaveCount(0);
  await expect.poll(async () => (await audioState(page)).t, { timeout: 8000 }).toBeGreaterThan(0.1);
  expect(await sessionState(page)).toBe("playing");
  // the queue position survived: the track after the resumed one is next
  await toEnd(page);
  await expect(bar.getByRole("link", { name: list[5]!.title })).toBeVisible({ timeout: 10_000 });
  // diagnostics reached the outbox through the listen-events op
  await expect.poll(async () => (await kinds()).filter((k) => k === "autoplay_blocked").length, { timeout: 10_000 }).toBeGreaterThanOrEqual(1);
  expect(await kinds()).toEqual(expect.arrayContaining(["advance", "complete", "autoplay_blocked"]));
});

test("any tap resumes a refused autoplay", async ({ page, request }) => {
  const list = await tracks(request);
  await startFirst(page, list, 6);
  await page.evaluate(() => ((window as unknown as { __blockPlay: boolean }).__blockPlay = true));
  await toEnd(page);
  await expect(page.getByTestId("player-continue")).toBeVisible({ timeout: 10_000 });
  await page.evaluate(() => ((window as unknown as { __blockPlay: boolean }).__blockPlay = false));
  await page.getByRole("heading", { name: "Library" }).click();
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause", { timeout: 8000 });
  await expect.poll(async () => (await audioState(page)).paused).toBe(false);
});

test("a stream whose ended never fires still advances (watchdog), once, with one complete", async ({ page, request }) => {
  // The watchdog is timer logic (500 ms interval, 1.5 s without progress at the end): page.clock drives it, so the
  // test never sleeps. The media itself is real and only has to play the last quarter second.
  await page.clock.install();
  const list = await tracks(request);
  const kinds = watchKinds(page);
  await startFirst(page, list, 7);
  await page.evaluate(() => ((window as unknown as { __noEnded: boolean }).__noEnded = true));
  // Hold the page's timers while the media reaches its end: with them flowing, a slow machine could let the
  // real-time watchdog fire before this test has looked, and the stream would already be gone.
  await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 1000);
  await toEnd(page, 0.25);
  // the stream sits at its end and `ended` never reached the page
  await expect
    .poll(async () => {
      const a = await audioState(page);
      return a.d - a.t;
    }, { timeout: 8000 })
    .toBeLessThan(0.05);
  const bar = page.getByTestId("player-bar");
  await page.clock.runFor(700); // a tick records the final position ...
  await page.clock.runFor(2000); // ... and the next ones see 1.5 s without progress
  await page.clock.resume();
  await expect(bar.getByRole("link", { name: list[8]!.title })).toBeVisible({ timeout: 15_000 });
  await page.evaluate(() => ((window as unknown as { __noEnded: boolean }).__noEnded = false));
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause");
  await expect.poll(async () => (await audioState(page)).t, { timeout: 8000 }).toBeGreaterThan(0.1);
  // not advanced twice: three more seconds of watchdog ticks change nothing
  await page.clock.fastForward(3000);
  await expect(bar.getByRole("link", { name: list[8]!.title })).toBeVisible();
  // The outbox persists asynchronously: the first IndexedDB open of a fresh Firefox profile can take seconds
  // under load, and every event queued meanwhile only becomes visible afterwards. Wait for the burst, then
  // assert exactness; the short real wait lets a late duplicate from a real event show up too.
  await expect.poll(async () => (await kinds()).filter((k) => k === "complete").length, { timeout: 20_000 }).toBeGreaterThanOrEqual(1);
  await page.waitForTimeout(500);
  const all = await kinds();
  expect(all.filter((k) => k === "complete")).toHaveLength(1);
  expect(all.filter((k) => k === "advance")).toHaveLength(1);
});

test("the end of the queue stops visibly and the Media Session goes to none", async ({ page, request }) => {
  const list = await tracks(request);
  const last = list.length - 1;
  await startFirst(page, list, last);
  await expect(page.getByTestId("player-bar").getByRole("button", { name: "Next", exact: true })).toBeDisabled();
  await toEnd(page);
  await expect(page.getByTestId("player-end")).toHaveText("End of queue", { timeout: 10_000 });
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Play");
  expect(await sessionState(page)).toBe("none");
  // play again restarts the last track and clears the state
  await page.getByTestId("play-toggle").click();
  await expect(page.getByTestId("player-end")).toHaveCount(0);
  await expect.poll(async () => (await audioState(page)).paused).toBe(false);
});

test("play next puts a track right after the current one", async ({ page, request }) => {
  const list = await tracks(request);
  await startFirst(page, list, 0);
  await page.getByRole("button", { name: `Queue ${list[5]!.title} next`, exact: true }).click();
  await toEnd(page);
  await expect(page.getByTestId("player-bar").getByRole("link", { name: list[5]!.title })).toBeVisible({ timeout: 10_000 });
  await expect.poll(async () => (await audioState(page)).paused).toBe(false);
});

test("@realtime strict autoplay: every track change starts or asks for a tap, never a silent stop", async ({ page, request }) => {
  // Firefox policy 2 refuses any play() that is not synchronous inside an input handler: the first play
  // (links are fetched first) and the one in `ended` may both be refused. The ladder must still work.
  // Real time on purpose: "Syn song 02" (2 s seed take) ends by itself, the refusal happens on a genuine `ended`.
  const list = await tracks(request);
  const i = list.findIndex((x) => x.title === "Syn song 02");
  const first = list[i];
  const second = list[i + 1];
  if (!first || !second) throw new Error("seed track Syn song 02 or its successor missing");
  const bar = page.getByTestId("player-bar");
  const toggle = page.getByTestId("play-toggle");
  const settle = async () => {
    await expect
      .poll(async () => ((await toggle.getAttribute("aria-label")) === "Pause" ? "playing" : (await page.getByTestId("player-continue").count()) > 0 ? "ask" : "silent"), { timeout: 10_000 })
      .not.toBe("silent");
    if ((await page.getByTestId("player-continue").count()) > 0) await page.getByTestId("player-continue").click();
    await expect(toggle).toHaveAttribute("aria-label", "Pause");
    await expect.poll(async () => (await audioState(page)).t, { timeout: 8000 }).toBeGreaterThan(0.1);
  };
  await page.goto("/");
  await page.getByRole("button", { name: `Play ${first.title}`, exact: true }).click();
  await settle();
  // no seeking: the take plays out and the player advances (or asks) on its own
  await expect(bar.getByRole("link", { name: second.title })).toBeVisible({ timeout: 15_000 });
  await settle();
});

test("playing from the track page queues the track's song group, so the next take starts by itself", async ({ page, request }) => {
  const list = await tracks(request);
  let found: { id: string; sibling: Item } | undefined;
  for (const t of list) {
    const d = (await (await request.get(`/api/tracks/${t.id}`)).json()) as { lineage: { siblings: Item[] } };
    const s = d.lineage.siblings[0];
    if (s) {
      found = { id: t.id, sibling: s };
      break;
    }
  }
  if (!found) throw new Error("the e2e profile has no track with a sibling take");
  await page.goto(`/tracks/${found.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audioState(page)).t, { timeout: 8000 }).toBeGreaterThan(0.2);
  await toEnd(page);
  await expect(page.getByTestId("player-bar").getByRole("link", { name: found.sibling.title })).toBeVisible({ timeout: 10_000 });
  await expect.poll(async () => (await audioState(page)).paused).toBe(false);
});

test("Media Session: nexttrack is withdrawn when nothing is next", async ({ page, request }) => {
  await page.addInitScript(() => {
    const spy: Record<string, string> = {};
    (window as unknown as { __msHandlers: Record<string, string> }).__msHandlers = spy;
    const ms = navigator.mediaSession;
    const orig = ms.setActionHandler.bind(ms);
    ms.setActionHandler = (action, handler) => {
      spy[action] = handler === null ? "null" : "fn";
      orig(action, handler);
    };
  });
  const list = await tracks(request);
  await startFirst(page, list, list.length - 1);
  await expect
    .poll(() => page.evaluate(() => (window as unknown as { __msHandlers: Record<string, string> }).__msHandlers["nexttrack"]))
    .toBe("null");
  await startFirst(page, list, 0);
  await expect
    .poll(() => page.evaluate(() => (window as unknown as { __msHandlers: Record<string, string> }).__msHandlers["nexttrack"]))
    .toBe("fn");
});
