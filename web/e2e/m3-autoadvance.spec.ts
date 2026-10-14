// Auto-advance hardening (Android Firefox: at the end of a song the next one did not start).
// The "firefox-android" project runs this spec with an Android Firefox user agent, a phone viewport,
// touch and Firefox's strict autoplay prefs; chrome and the Pixel 7 project run the same specs.
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

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

/** Listen-event kinds the SPA queued or already sent. */
function watchKinds(page: Page) {
  const sent: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname === "/api/listen-events" && r.postData()) {
      for (const e of (JSON.parse(r.postData() ?? "{}") as { events?: { kind: string }[] }).events ?? []) sent.push(e.kind);
    }
  });
  const queued = () =>
    page.evaluate(
      () =>
        new Promise<string[]>((resolve) => {
          const open = indexedDB.open("mm-outbox", 1);
          open.onerror = () => resolve([]);
          open.onsuccess = () => {
            const db = open.result;
            if (!db.objectStoreNames.contains("events")) return resolve([]);
            const r = db.transaction("events", "readonly").objectStore("events").getAll();
            r.onsuccess = () => resolve((r.result as { event: { kind: string } }[]).map((x) => x.event.kind));
            r.onerror = () => resolve([]);
          };
        }),
    );
  return async () => [...sent, ...(await queued())];
}

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
  await expect.poll(async () => (await audioState(page)).t, { timeout: 8000 }).toBeGreaterThan(0.2);
  return t;
}

const toEnd = (page: Page, before = 0.5) =>
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
  await expect.poll(async () => (await audioState(page)).t, { timeout: 8000 }).toBeGreaterThan(0.3);
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
  const list = await tracks(request);
  const kinds = watchKinds(page);
  await startFirst(page, list, 7);
  await page.evaluate(() => ((window as unknown as { __noEnded: boolean }).__noEnded = true));
  await toEnd(page, 0.25);
  const bar = page.getByTestId("player-bar");
  await expect(bar.getByRole("link", { name: list[8]!.title })).toBeVisible({ timeout: 15_000 });
  await page.evaluate(() => ((window as unknown as { __noEnded: boolean }).__noEnded = false));
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause");
  await expect.poll(async () => (await audioState(page)).t, { timeout: 8000 }).toBeGreaterThan(0.3);
  // not advanced twice
  await page.waitForTimeout(2500);
  await expect(bar.getByRole("link", { name: list[8]!.title })).toBeVisible();
  // The outbox persists asynchronously: the first IndexedDB open of a fresh Firefox profile can take seconds
  // under load, and every event queued meanwhile only becomes visible afterwards. Wait for the burst, then
  // assert exactness (the earlier 2.5 s wait and the poll tail make a duplicate show up too).
  await expect.poll(async () => (await kinds()).filter((k) => k === "complete").length, { timeout: 20_000 }).toBeGreaterThanOrEqual(1);
  await page.waitForTimeout(1000);
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

test("strict autoplay: every track change starts or asks for a tap, never a silent stop", async ({ page, request }) => {
  // Firefox policy 2 refuses any play() that is not synchronous inside an input handler: the first play
  // (links are fetched first) and the one in `ended` may both be refused. The ladder must still work.
  const list = await tracks(request);
  const bar = page.getByTestId("player-bar");
  const toggle = page.getByTestId("play-toggle");
  const settle = async () => {
    await expect
      .poll(async () => ((await toggle.getAttribute("aria-label")) === "Pause" ? "playing" : (await page.getByTestId("player-continue").count()) > 0 ? "ask" : "silent"), { timeout: 10_000 })
      .not.toBe("silent");
    if ((await page.getByTestId("player-continue").count()) > 0) await page.getByTestId("player-continue").click();
    await expect(toggle).toHaveAttribute("aria-label", "Pause");
    await expect.poll(async () => (await audioState(page)).t, { timeout: 8000 }).toBeGreaterThan(0.3);
  };
  await page.goto("/");
  await page.getByRole("button", { name: `Play ${list[1]!.title}`, exact: true }).click();
  await settle();
  await toEnd(page);
  await expect(bar.getByRole("link", { name: list[2]!.title })).toBeVisible({ timeout: 10_000 });
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
