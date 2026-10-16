// matrix: @mobile @firefox @realtime because it covers the PWA manifest, Media Session, session-loss banner and the device codec probe
import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { SEED_TRACKS } from "./env";

async function trackId(request: APIRequestContext, title: string): Promise<string> {
  const r = await request.get("/api/tracks?limit=50");
  const items = ((await r.json()) as { items: { id: string; title: string }[] }).items;
  const t = items.find((x) => x.title === title);
  if (!t) throw new Error(`seed track ${title} missing`);
  return t.id;
}

const now = (page: Page) => page.evaluate(() => document.querySelector("audio")?.currentTime ?? -1);
// false while a reload is in flight (the execution context is gone) or once the marker is lost
const alive = (page: Page) =>
  page.evaluate(() => (window as unknown as { __alive?: boolean }).__alive === true).catch(() => false);
const markAlive = (page: Page) =>
  page.evaluate(() => {
    (window as unknown as { __alive?: boolean }).__alive = true;
  });

test("manifest and icons load without auth", async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, extraHTTPHeaders: {} });
  try {
    // the context really has no identity
    expect((await ctx.request.get("/api/me")).status()).toBe(401);
    const m = await ctx.request.get("/manifest.webmanifest");
    expect(m.status()).toBe(200);
    expect(m.headers()["content-type"]).toMatch(/application\/(manifest\+json|json)/);
    const manifest = (await m.json()) as { icons: { src: string; type: string }[]; scope: string; display: string };
    expect(manifest.display).toBe("standalone");
    expect(manifest.scope).toBe("/");
    for (const icon of manifest.icons) {
      const r = await ctx.request.get(icon.src);
      expect(r.status(), icon.src).toBe(200);
      expect(r.headers()["content-type"], icon.src).toContain(icon.type);
    }
    const apple = await ctx.request.get("/icons/apple-touch-icon.png");
    expect(apple.status()).toBe(200);
    expect(apple.headers()["content-type"]).toContain("image/png");
  } finally {
    await ctx.close();
  }
});

test("media session handlers and metadata", async ({ page, request }) => {
  await page.addInitScript(() => {
    const spy: Record<string, string> = {};
    (window as unknown as { __msHandlers: Record<string, string> }).__msHandlers = spy;
    const ms = navigator.mediaSession;
    if (!ms) return;
    const orig = ms.setActionHandler.bind(ms);
    ms.setActionHandler = (action, handler) => {
      spy[action] = handler === null ? "null" : "fn";
      orig(action, handler);
    };
  });
  const id = await trackId(request, "Syn song 01 s1001");
  await page.goto(`/tracks/${id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(() => now(page), { timeout: 5000 }).toBeGreaterThan(0.2);
  const handlers = await page.evaluate(() => (window as unknown as { __msHandlers: Record<string, string> }).__msHandlers);
  // "Syn song 01 s1001" has a sibling take, so the queue has a next track and nexttrack stays registered
  // (a track without a next one withdraws it, see m3-autoadvance.spec.ts).
  for (const a of ["play", "pause", "previoustrack", "nexttrack", "seekto"]) expect(handlers[a], a).toBe("fn");
  for (const a of ["seekforward", "seekbackward"]) expect(handlers[a] ?? "null", a).toBe("null");
  const meta = await page.evaluate(() => {
    const m = navigator.mediaSession.metadata;
    return m ? { title: m.title, artist: m.artist, album: m.album, artwork: m.artwork.map((a) => a.src) } : null;
  });
  expect(meta?.title).toBe("Syn song 01 s1001");
  expect(meta?.artist).toBe("metamusic");
  // C115: the cover art (512 and 1024), the app icon only for a track without a cover
  expect(meta?.artwork.join(",")).toMatch(/\/media\/[0-9a-f]{64}\.png\?exp=/);
});

test.describe("session loss", () => {
  test("401 while paused reloads", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("track-row")).toHaveCount(SEED_TRACKS);
    await markAlive(page);
    let first = true;
    await page.route("**/api/system", (route) => {
      if (first) {
        first = false;
        return route.fulfill({ status: 401, contentType: "application/problem+json", body: '{"code":"UNAUTHENTICATED","message":"x","details":{}}' });
      }
      return route.continue();
    });
    await page.getByRole("link", { name: "Settings" }).click();
    await expect.poll(() => alive(page)).toBe(false); // full reload
    await expect(page.getByTestId("system-info")).toBeVisible();
    await expect(page.getByTestId("session-banner")).toHaveCount(0);
  });

  test("401 while playing shows the banner and never reloads", async ({ page, request }) => {
    const id = await trackId(request, "Syn song 03");
    await page.goto(`/tracks/${id}`);
    await page.getByTestId("track-play").click();
    await expect.poll(() => now(page), { timeout: 5000 }).toBeGreaterThan(0.2);
    await markAlive(page);
    await page.route("**/api/system", (route) =>
      route.fulfill({ status: 401, contentType: "application/problem+json", body: '{"code":"UNAUTHENTICATED","message":"x","details":{}}' }),
    );
    await page.getByRole("link", { name: "Settings" }).click();
    await expect(page.getByTestId("session-banner")).toBeVisible();
    expect(await alive(page)).toBe(true);
    const t = await now(page);
    await expect.poll(() => now(page), { timeout: 4000 }).toBeGreaterThan(t);
    expect(await alive(page)).toBe(true);
  });

  test("one aborted request then success does not reload", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("track-row")).toHaveCount(SEED_TRACKS);
    await markAlive(page);
    let first = true;
    await page.route("**/api/system", (route) => {
      if (first) {
        first = false;
        return route.abort("connectionreset");
      }
      return route.continue();
    });
    await page.getByRole("link", { name: "Settings" }).click();
    await expect(page.getByTestId("system-info")).toBeVisible({ timeout: 10_000 });
    expect(await alive(page)).toBe(true);
    await expect(page.getByTestId("session-banner")).toHaveCount(0);
  });

  test("two aborts and a redirecting ping reload while paused", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("track-row")).toHaveCount(SEED_TRACKS);
    await markAlive(page);
    let aborts = 0;
    await page.route("**/api/system", (route) => {
      if (aborts < 2) {
        aborts++;
        return route.abort("connectionreset");
      }
      return route.continue();
    });
    // the edge answers the confirmation probe with a login redirect
    let pings = 0;
    await page.route("**/api/ping", (route) => {
      if (pings++ === 0) return route.fulfill({ status: 302, headers: { location: "/auth/login" }, body: "" });
      return route.continue();
    });
    await page.getByRole("link", { name: "Settings" }).click();
    await expect.poll(() => alive(page), { timeout: 10_000 }).toBe(false);
    await expect(page.getByTestId("system-info")).toBeVisible();
  });

  test("a second confirmed loss within 30 s shows the banner", async ({ page }) => {
    let calls = 0;
    await page.route("**/api/system", (route) => {
      calls++;
      return route.fulfill({ status: 401, contentType: "application/problem+json", body: '{"code":"UNAUTHENTICATED","message":"x","details":{}}' });
    });
    await page.goto("/settings");
    await expect(page.getByTestId("session-banner")).toBeVisible({ timeout: 10_000 });
    expect(calls).toBe(2); // first load -> reload; second load -> banner instead of a loop
    await page.waitForTimeout(1500);
    expect(calls).toBe(2);
  });
});

test.describe("device probe", () => {
  async function probePath(request: APIRequestContext): Promise<string> {
    const r = await request.get("/api/device-probe");
    return new URL((await r.json()).url as string, "http://x").pathname;
  }
  const profileRow = (page: Page) => page.locator("table[data-testid=device-profiles] tr.current");

  test("click Play runs the probe and stores ok", async ({ page, request }) => {
    const id = await trackId(request, "Syn song 03");
    await page.goto(`/tracks/${id}`);
    await page.getByTestId("track-play").click();
    await expect.poll(() => now(page), { timeout: 5000 }).toBeGreaterThan(0.2);
    await page.getByRole("link", { name: "Settings" }).click();
    await expect(page.getByTestId("probe-result")).toHaveText("ok", { timeout: 10_000 });
    await expect(profileRow(page)).toContainText("ok");
    await expect(page.getByTestId("quality-source")).toContainText("lossless");
  });

  test("a failing probe stores failed, downgrades to mp3", async ({ page, request }) => {
    const sha = (await probePath(request)).replace(/^\/media\//, "").replace(/\..*$/, "");
    await page.route(new RegExp(`/media/${sha}\\.flac`), (route) =>
      route.fulfill({ status: 200, contentType: "audio/flac", body: Buffer.from(Array.from({ length: 4096 }, () => Math.floor(Math.random() * 256))) }),
    );
    const id = await trackId(request, "Syn song 03");
    await page.goto(`/tracks/${id}`);
    await page.getByTestId("track-play").click();
    await expect.poll(() => now(page), { timeout: 5000 }).toBeGreaterThan(0.2);
    await page.getByRole("link", { name: "Settings" }).click();
    await expect(page.getByTestId("probe-result")).toHaveText("failed", { timeout: 15_000 });
    await expect(page.getByTestId("quality-source")).toContainText("high (probe)");
    await expect(profileRow(page)).toContainText("failed");

    // the next play uses the MP3
    const id2 = await trackId(request, "Syn song 05");
    await page.goto(`/tracks/${id2}`);
    await page.getByTestId("track-play").click();
    await expect.poll(() => page.evaluate(() => document.querySelector("audio")?.currentSrc ?? "")).toMatch(/\/media\/[0-9a-f]{64}\.mp3\?exp=/);
  });

  test("a silent probe is inconclusive and changes nothing", async ({ page, request }) => {
    test.setTimeout(60_000);
    const sha = (await probePath(request)).replace(/^\/media\//, "").replace(/\..*$/, "");
    await page.route(new RegExp(`/media/${sha}\\.flac`), () => {
      /* never answered */
    });
    const id = await trackId(request, "Syn song 03");
    await page.goto(`/tracks/${id}`);
    const stored = page.waitForResponse((r) => r.request().method() === "PUT" && r.url().includes("/api/device-profiles/"), { timeout: 30_000 });
    await page.getByTestId("track-play").click();
    await expect.poll(() => now(page), { timeout: 5000 }).toBeGreaterThan(0.2);
    expect((await stored).status()).toBe(200); // written when the 10 s probe timeout fires
    await page.getByRole("link", { name: "Settings" }).click();
    await expect(profileRow(page)).toContainText("inconclusive");
    await expect(page.getByTestId("probe-result")).toHaveText("none");
    await expect(page.getByTestId("quality-source")).toContainText("lossless (default)");
  });
});
