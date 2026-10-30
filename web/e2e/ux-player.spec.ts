// matrix: @mobile because the now-playing sheet, Android Back, drag-to-close, the inert page behind the sheet and the reload restore are phone behaviours (desktop runs the panel and the queue/persistence parts)
// Wave 3a: now-playing sheet, queue editing, playback that survives reloads, duels that borrow and return audio focus.
import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { allTracks, openNav, spaGoto, type Item } from "./helpers";
import { createDuel, siblings } from "./m2bApi";

const isPhone = (page: Page) => (page.viewportSize()?.width ?? 1280) < 768;

const audio = (page: Page) =>
  page.evaluate(() => {
    const a = document.querySelector("audio");
    return { t: a?.currentTime ?? -1, paused: a?.paused ?? true, src: a?.getAttribute("src") ?? "" };
  });

/**
 * Four takes of 7 s or more that sit in different queues: a long first take (its page queues only itself), then three
 * more. "Syn song 01 s2001" has a lineage sibling, which is why it comes last and is only ever added.
 */
async function four(request: APIRequestContext): Promise<[Item, Item, Item, Item]> {
  const all = await allTracks(request);
  const pick = (title: string): Item => {
    const t = all.find((x) => x.title === title);
    if (!t || t.duration_s < 6) throw new Error(`the e2e profile needs ${title} (6 s or more)`);
    return t;
  };
  return [pick("Syn song 03"), pick("Syn song 05"), pick("Syn song 04"), pick("Syn song 01 s2001")];
}

/** Play `a` from its track page, then add the others to the queue from their pages ("Add to queue"). */
async function queueUp(page: Page, items: Item[]): Promise<void> {
  const [a, ...rest] = items;
  if (!a) throw new Error("nothing to queue");
  await page.goto(`/tracks/${a.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.2);
  for (const t of rest) {
    await spaGoto(page, `/tracks/${t.id}`);
    await expect(page.getByTestId("track-title")).toHaveText(t.title);
    await page.getByTestId("add-to-queue").click();
    await expect(page.getByTestId("toast").filter({ hasText: `Added to queue: ${t.title}` })).toBeVisible();
  }
}

const openSheet = async (page: Page) => {
  await page.getByTestId("player-open-sheet").click();
  await expect(page.getByTestId("now-playing")).toBeVisible();
};

test("opening and closing the sheet never pauses", async ({ page, request }) => {
  const [a] = await four(request);
  await page.goto(`/tracks/${a.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.2);

  await openSheet(page);
  const dlg = page.getByTestId("now-playing");
  await expect(dlg).toHaveAttribute("role", "dialog");
  await expect(dlg).toHaveAttribute("aria-modal", isPhone(page) ? "true" : "false");
  await expect(dlg.getByTestId("np-title")).toHaveText(a.title);
  await expect(page.getByTestId("np-close")).toBeFocused(); // focus moves in
  expect((await audio(page)).paused).toBe(false);

  // the judging controls: 52 px stars, status chips instead of the select
  // (stars are 52 px on touch devices; a fine pointer gets the compact size)
  // (sub-pixel layout: a 52 px token can measure 51.999969, so compare with a half-pixel tolerance)
  if (isPhone(page)) expect((await dlg.getByRole("button", { name: "3 stars" }).boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(52 - 0.5);
  await expect(dlg.getByRole("button", { name: "To relisten" })).toBeVisible();
  await expect(dlg.getByRole("button", { name: "Final" })).toBeVisible();
  await expect(dlg.getByRole("button", { name: "Discard" })).toBeVisible();
  await expect(dlg.getByTestId("status-select")).toHaveCount(0);
  await expect(dlg.getByTestId("np-comment")).toContainText("Comment at ");
  await expect(dlg.getByRole("link", { name: "Open take" })).toBeVisible();

  // button
  await page.getByTestId("np-close").click();
  await expect(dlg).toHaveCount(0);
  await expect(page.getByTestId("player-open-sheet")).toBeFocused(); // focus returns to the opener
  expect((await audio(page)).paused).toBe(false);

  // Escape
  await openSheet(page);
  await page.keyboard.press("Escape");
  await expect(dlg).toHaveCount(0);
  expect((await audio(page)).paused).toBe(false);

  if (isPhone(page)) {
    // scrim
    await openSheet(page);
    await page.getByTestId("np-scrim").click({ position: { x: 10, y: 10 } });
    await expect(dlg).toHaveCount(0);
    // drag the handle down past 30 % of the sheet
    await openSheet(page);
    const box = (await dlg.boundingBox())!;
    const grab = (await page.getByTestId("np-grab").boundingBox())!;
    await page.mouse.move(grab.x + grab.width / 2, grab.y + grab.height / 2);
    await page.mouse.down();
    await page.mouse.move(grab.x + grab.width / 2, grab.y + grab.height / 2 + box.height * 0.1, { steps: 4 });
    await page.mouse.up();
    await expect(dlg).toBeVisible(); // 10 % is not enough: it snaps back
    const g2 = (await page.getByTestId("np-grab").boundingBox())!;
    await page.mouse.move(g2.x + g2.width / 2, g2.y + g2.height / 2);
    await page.mouse.down();
    await page.mouse.move(g2.x + g2.width / 2, g2.y + g2.height / 2 + box.height * 0.5, { steps: 6 });
    await page.mouse.up();
    await expect(dlg).toHaveCount(0);
    expect((await audio(page)).paused).toBe(false);
  }
});

test("Android Back closes the sheet and stays on the page", async ({ page, request }) => {
  test.skip(!isPhone(page), "the sheet is a history entry on the phone layout only");
  const [a] = await four(request);
  await page.goto(`/tracks/${a.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.2);
  const url = page.url();
  const len = await page.evaluate(() => history.length);

  await openSheet(page);
  expect(await page.evaluate(() => history.length)).toBe(len + 1);
  expect(await page.evaluate(() => (history.state as { mmSheet?: string } | null)?.mmSheet)).toBe("now");

  await page.goBack();
  await expect(page.getByTestId("now-playing")).toHaveCount(0);
  expect(page.url()).toBe(url);
  await expect(page.getByTestId("track-title")).toHaveText(a.title); // the page behind is untouched
  expect((await audio(page)).paused).toBe(false);

  // Forward reopens it; a close from the UI is a Back as well
  await page.goForward();
  await expect(page.getByTestId("now-playing")).toBeVisible();
  await page.getByTestId("np-close").click();
  await expect(page.getByTestId("now-playing")).toHaveCount(0);
  expect(page.url()).toBe(url);

  // Open take from inside the sheet replaces the sheet's entry: one Back returns to where we were
  await spaGoto(page, "/queue");
  await openSheet(page);
  await page.getByTestId("np-open-take").click();
  await expect(page).toHaveURL(new RegExp(`/tracks/${a.id}$`));
  await expect(page.getByTestId("now-playing")).toHaveCount(0);
  await page.goBack();
  await expect(page).toHaveURL(/\/queue$/);
  expect((await audio(page)).paused).toBe(false);
});

test("queue: remove the next take and it is skipped; reorder; clear upcoming; announced", async ({ page, request }) => {
  const [a, b, c, d] = await four(request);
  await queueUp(page, [a, b, c, d]);
  await openNav(page, "Play queue");
  const items = page.getByTestId("queue-item");
  await expect(items).toHaveCount(3);
  await expect(items.nth(0)).toContainText(b.title);

  // announced in the polite live region, 2 s toast
  const live = page.locator('[role="status"][aria-live="polite"]');
  await items.nth(1).getByTestId("queue-up").click();
  await expect(live.getByTestId("toast").filter({ hasText: "Moved up" })).toBeVisible();
  await expect(items.nth(0)).toContainText(c.title);
  await expect(items.nth(1)).toContainText(b.title);
  await items.nth(0).getByTestId("queue-down").click();
  await expect(items.nth(0)).toContainText(b.title);
  await expect(items.nth(0).getByTestId("queue-up")).toBeDisabled();
  await expect(items.nth(2).getByTestId("queue-down")).toBeDisabled();

  // remove the next take
  await items.nth(0).getByTestId("queue-remove").click();
  await expect(live.getByTestId("toast").filter({ hasText: `Removed from the queue: ${b.title}` })).toBeVisible();
  await expect(items).toHaveCount(2);
  await expect(items.nth(0)).toContainText(c.title);
  await expect(page.getByTestId("toast").first()).toBeHidden({ timeout: 4000 }); // info toasts last about 2 s

  // Next skips it
  await page.getByTestId("player-bar").getByRole("button", { name: "Next", exact: true }).click();
  await expect(page.getByTestId("queue-now")).toContainText(c.title);
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.1);

  // Clear upcoming
  await page.getByTestId("queue-clear").click();
  await expect(page.getByTestId("queue-end")).toBeVisible();
  await expect(page.getByTestId("player-bar").getByRole("button", { name: "Next", exact: true })).toBeDisabled();
  expect((await audio(page)).paused).toBe(false);
});

test("the sheet shows the queue and edits it", async ({ page, request }) => {
  const [a, b, c] = await four(request);
  await queueUp(page, [a, b, c]);
  await openSheet(page);
  await page.getByTestId("np-queue-toggle").click();
  const dlg = page.getByTestId("now-playing");
  await expect(dlg.getByTestId("queue-item")).toHaveCount(2);
  await dlg.getByTestId("queue-item").first().getByTestId("queue-remove").click();
  await expect(dlg.getByTestId("queue-item")).toHaveCount(1);
  await expect(dlg.getByTestId("queue-item")).toContainText(c.title);
  expect((await audio(page)).paused).toBe(false);
});

test("a duel pauses the player and the verdict resumes it", async ({ page, request }) => {
  const [a] = await four(request);
  const { root, child } = await siblings(request);
  const duel = await createDuel(request, root.id, child.id);
  await page.goto(`/tracks/${a.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.2);

  await spaGoto(page, `/duels/${duel.id}`);
  await expect(page.getByTestId("ab-play")).toBeVisible({ timeout: 20_000 });
  expect((await audio(page)).paused).toBe(false); // the page alone does not take the audio
  await page.getByTestId("ab-play").click();
  await expect.poll(async () => (await audio(page)).paused).toBe(true);
  const at = (await audio(page)).t;

  await page.getByTestId("verdict-a").click();
  await page.getByTestId("verdict-submit").click();
  await expect.poll(async () => (await audio(page)).paused, { timeout: 10_000 }).toBe(false);
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(at + 0.2);
  await expect(page.getByTestId("player-resume-hold")).toHaveCount(0);
});

test("leaving the duel route hands the audio back too", async ({ page, request }) => {
  const [a] = await four(request);
  const { root, child } = await siblings(request);
  const duel = await createDuel(request, root.id, child.id);
  await page.goto(`/tracks/${a.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.2);
  await spaGoto(page, `/duels/${duel.id}`);
  await expect(page.getByTestId("ab-play")).toBeVisible({ timeout: 20_000 });
  await page.getByTestId("ab-play").click();
  await expect.poll(async () => (await audio(page)).paused).toBe(true);

  await openNav(page, "Library"); // a tap on the tab bar / top nav: inside a gesture
  await expect.poll(async () => (await audio(page)).paused, { timeout: 10_000 }).toBe(false);
  await expect(page.getByTestId("player-resume-hold")).toHaveCount(0);
});

test("a reload restores the queue paused with Resume at m:ss, and one tap plays", async ({ page, request }) => {
  const [a, b] = await four(request);
  await queueUp(page, [a, b]);
  await spaGoto(page, `/tracks/${a.id}`);
  await page.evaluate(() => {
    const el = document.querySelector("audio");
    if (el) el.currentTime = 4;
  });
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThanOrEqual(4);
  await page.getByTestId("track-play").click(); // pause so the clock does not drift
  await expect.poll(async () => (await audio(page)).paused).toBe(true);
  const pos = (await audio(page)).t;

  await page.reload();
  const resume = page.getByTestId("player-resume");
  await expect(resume).toBeVisible();
  await expect(resume).toContainText(`Resume ${a.title} at `);
  const m = /at (\d+):(\d\d)/.exec((await resume.textContent()) ?? "");
  expect(m).not.toBeNull();
  const shown = Number(m![1]) * 60 + Number(m![2]);
  expect(Math.abs(shown - pos), `shown ${shown} s, paused at ${pos} s`).toBeLessThanOrEqual(1);
  // restored paused: no autoplay, no media applied, nothing but ids in storage
  const st = await audio(page);
  expect(st.paused).toBe(true);
  expect(st.src).toBe("");
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Play");
  const raw = await page.evaluate(() => localStorage.getItem("mm:player:v1") ?? "");
  expect(raw).toContain(a.id);
  expect(raw).not.toMatch(/media|https?:|sig=|exp=/);
  // the queue came back with it
  await openNav(page, "Play queue");
  await expect(page.getByTestId("queue-now")).toContainText(a.title);
  await expect(page.getByTestId("queue-item")).toHaveCount(1);
  await expect(page.getByTestId("queue-item")).toContainText(b.title);

  await page.getByTestId("player-resume").click();
  await expect.poll(async () => (await audio(page)).paused, { timeout: 10_000 }).toBe(false);
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(pos - 1);
  await expect(page.getByTestId("player-resume")).toHaveCount(0);
});

test("a ?t= deep link of another take does not replace the queue", async ({ page, request }) => {
  const [a, b, c] = await four(request);
  await queueUp(page, [a, b]);
  const before = (await audio(page)).t;

  // while playing: in-app deep link to c
  await spaGoto(page, `/tracks/${c.id}?t=3`);
  await expect(page.getByTestId("track-title")).toHaveText(c.title);
  await expect(page).toHaveURL(new RegExp(`/tracks/${c.id}$`)); // the param is dropped
  const bar = page.getByTestId("player-bar");
  await expect(bar.getByRole("link", { name: new RegExp(a.title) })).toBeVisible();
  expect((await audio(page)).paused).toBe(false);
  expect((await audio(page)).t).toBeGreaterThanOrEqual(before);

  // after a reload (restored, paused) as well
  await page.getByTestId("play-toggle").click();
  await expect.poll(async () => (await audio(page)).paused).toBe(true);
  await page.goto(`/tracks/${c.id}?t=3`);
  await expect(page.getByTestId("track-title")).toHaveText(c.title);
  await expect(page.getByTestId("player-resume")).toContainText(`Resume ${a.title}`);
  await openNav(page, "Play queue");
  await expect(page.getByTestId("queue-now")).toContainText(a.title);
  await expect(page.getByTestId("queue-item")).toContainText(b.title);
});

test("Play on another take replaces the queue and offers Undo", async ({ page, request }) => {
  const [a, b, c] = await four(request);
  await queueUp(page, [a, b]);
  await spaGoto(page, `/tracks/${c.id}`);
  await expect(page.getByTestId("track-title")).toHaveText(c.title);
  await page.getByTestId("track-play").click();
  const toast = page.getByTestId("toast").filter({ hasText: "Queue replaced" });
  await expect(toast).toBeVisible();
  await expect(page.getByTestId("player-bar").getByRole("link", { name: new RegExp(c.title) })).toBeVisible();
  await toast.getByTestId("toast-action").click();
  await expect(page.getByTestId("player-bar").getByRole("link", { name: new RegExp(a.title) })).toBeVisible();
  await openNav(page, "Play queue");
  await expect(page.getByTestId("queue-now")).toContainText(a.title);
  await expect(page.getByTestId("queue-item")).toContainText(b.title);
});

// ---- wave 4b: sheet and toast polish, restored queue paging

const overlaps = (a: { x: number; y: number; width: number; height: number }, b: { x: number; y: number; width: number; height: number }) =>
  a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;

test("a toast over the open sheet sits clear of the transport and stars, and the page behind is inert", async ({ page, request }) => {
  const [a] = await four(request);
  await page.goto(`/tracks/${a.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.2);
  await openSheet(page);
  const dlg = page.getByTestId("now-playing");
  await expect(dlg.getByTestId("np-judge")).toBeVisible();

  if (isPhone(page)) {
    for (const sel of ["main", '[data-testid="topbar"]', "footer"]) await expect(page.locator(sel).first()).toHaveAttribute("inert", "");
  } else {
    await expect(page.locator("main")).not.toHaveAttribute("inert", "");
  }

  // a failed save raises an error toast while the sheet is open
  await page.route("**/api/tracks/*/rating", (r) => r.fulfill({ status: 500, contentType: "application/problem+json", body: JSON.stringify({ code: "INTERNAL", message: "no save", details: null }) }));
  await dlg.getByRole("button", { name: "4 stars" }).click();
  const toast = page.getByTestId("toast").first();
  await expect(toast).toBeVisible();
  const tb = (await toast.boundingBox())!;
  for (const id of ["np-play", "np-judge"]) {
    const b = (await dlg.getByTestId(id).boundingBox())!;
    expect(overlaps(tb, b), `the toast ${JSON.stringify(tb)} covers ${id} ${JSON.stringify(b)}`).toBe(false);
  }
  expect(tb.y).toBeLessThan(120); // the top of the viewport
  await page.unroute("**/api/tracks/*/rating");

  await page.getByTestId("np-close").click();
  await expect(page.locator("main")).not.toHaveAttribute("inert", "");
  await expect(page.locator("footer").first()).not.toHaveAttribute("inert", "");
});

test("a restored Library queue keeps paging", async ({ page, request }) => {
  const [a, b] = await four(request);
  await page.goto("/library");
  await page.getByRole("button", { name: `Play ${a.title}`, exact: true }).click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.2);
  await page.getByTestId("play-toggle").click(); // pause: nothing drifts
  await expect.poll(async () => (await audio(page)).paused).toBe(true);
  // the saved source is the Library page; cut the saved queue to two takes so only paging can bring the others back
  await expect.poll(() => page.evaluate(() => localStorage.getItem("mm:player:v1") ?? "")).toContain('"source":{"label":"Library"');
  // edit the storage from a second tab: this page's own pagehide would save its full queue over the edit
  const tab = await page.context().newPage();
  await tab.goto("/library");
  await tab.evaluate(
    ([x, y]) => {
      const s = JSON.parse(localStorage.getItem("mm:player:v1") ?? "{}") as Record<string, unknown>;
      s.queue = [x, y];
      s.index = 0;
      s.queuedNext = 0;
      s.trackId = x;
      s.position = 0;
      localStorage.setItem("mm:player:v1", JSON.stringify(s));
    },
    [a.id, b.id],
  );
  await tab.reload();
  await expect(tab.getByTestId("player-resume")).toBeVisible();
  await openNav(tab, "Play queue");
  await expect(tab.getByTestId("queue-now")).toContainText(a.title);
  // one tap resumes; the saved queue ends right after b, so the player asks the rebuilt source for the next page of the Library
  await tab.getByTestId("player-resume").click();
  await expect.poll(() => tab.getByTestId("queue-item").count(), { timeout: 15_000 }).toBeGreaterThan(2);
  await expect(tab.getByTestId("queue-item").first()).toContainText(b.title);
});

test("U1: tapping Play while \"Tap to continue\" shows resumes once, in the mini-player and the sheet", async ({ page, request }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { __blockPlay: boolean };
    w.__blockPlay = false;
    const orig = HTMLMediaElement.prototype.play;
    HTMLMediaElement.prototype.play = function (this: HTMLMediaElement) {
      if (w.__blockPlay) return Promise.reject(new DOMException("blocked by autoplay policy", "NotAllowedError"));
      return orig.call(this);
    };
  });
  // a take no other test in this file plays: listen history and resume positions are shared server state
  const a = (await allTracks(request)).find((x) => x.title === "Syn song 02");
  if (!a) throw new Error("the e2e profile needs Syn song 02");
  await page.goto("/library");
  await page.getByRole("button", { name: `Play ${a.title}`, exact: true }).click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.2);
  await page.evaluate(() => {
    const el = document.querySelector("audio");
    if (el) el.loop = true;
  });
  const block = (on: boolean) => page.evaluate((v) => ((window as unknown as { __blockPlay: boolean }).__blockPlay = v), on);
  const settled = async () => {
    await expect(page.getByTestId("player-continue")).toHaveCount(0);
    await expect.poll(async () => (await audio(page)).paused, { timeout: 5000 }).toBe(false);
    await page.waitForTimeout(400); // a second, wrongly queued pause would land here
    expect((await audio(page)).paused).toBe(false);
  };
  // round 1: the footer toggle
  await page.getByTestId("play-toggle").click(); // a real user pause
  await expect.poll(async () => (await audio(page)).paused).toBe(true);
  await block(true);
  await page.getByTestId("play-toggle").click(); // refused: "Tap to continue"
  await expect(page.getByTestId("player-continue")).toBeVisible();
  await block(false);
  await page.getByTestId("play-toggle").click();
  await settled();
  // round 2: the Now Playing sheet's toggle (the sheet is already open when the refusal happens)
  await openSheet(page);
  await page.getByTestId("np-play").click();
  await expect.poll(async () => (await audio(page)).paused).toBe(true);
  await block(true);
  await page.getByTestId("np-play").click();
  await expect(page.getByTestId("player-continue")).toBeVisible();
  await block(false);
  await page.getByTestId("np-play").click();
  await settled();
  await page.getByTestId("np-play").click(); // leave nothing playing
  await expect.poll(async () => (await audio(page)).paused).toBe(true);
});
