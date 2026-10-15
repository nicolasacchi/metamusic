import { expect, test, type APIRequestContext, type BrowserContext, type Page } from "@playwright/test";
import { allTracks, audioTime, apiJSON } from "./helpers";

// Offline listening (C115, C117.6). Project `chrome` only: the worker is allowed here (and in m4a-track) and
// everything else runs with workers blocked. The tests change the global offline set, so they run in order and the
// one that edits it puts it back.
test.use({ serviceWorkers: "allow" });
test.describe.configure({ mode: "serial" });

const CHUNK = 2 * 1024 * 1024;
const CAP = 4 * 1024 * 1024;

interface SetItem {
  track_id: string;
  title: string;
  duration_s: number;
}
interface OfflineSet {
  playlist_id: string;
  revision: number;
  items: SetItem[];
}

const getSet = (request: APIRequestContext) => apiJSON<OfflineSet>(request, "GET", "/api/offline-set");
const controlled = (page: Page) => page.evaluate(() => navigator.serviceWorker.controller !== null);

/** Wraps StorageManager.persist() so the spec can count the calls. */
async function spyPersist(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as { __persist: number };
    w.__persist = 0;
    const proto = StorageManager.prototype;
    const orig = proto.persist;
    proto.persist = function (this: StorageManager) {
      w.__persist++;
      return orig.call(this);
    };
  });
}

/** Offline store facts: the files in IndexedDB and how many chunks of each are in Cache Storage. */
const storeFacts = (page: Page) =>
  page.evaluate(async () => {
    const db = await new Promise<IDBDatabase>((res, rej) => {
      const o = indexedDB.open("mm-offline", 1);
      o.onsuccess = () => res(o.result);
      o.onerror = () => rej(o.error);
    });
    const files = await new Promise<{ sha: string; ext: string; bytes: number; chunks: number; complete: boolean }[]>((res, rej) => {
      const r = db.transaction("files", "readonly").objectStore("files").getAll();
      r.onsuccess = () => res(r.result);
      r.onerror = () => rej(r.error);
    });
    db.close();
    const cache = await caches.open("mm-media-v1");
    const keys = (await cache.keys()).map((k) => new URL(k.url).pathname);
    // covers are stored as files too (png); the audio is what the assertions are about
    return files.filter((f) => f.ext !== "png").map((f) => ({
      ...f,
      cached: keys.filter((k) => k.startsWith(`/__offline/${f.sha}.${f.ext}/`)).length,
    }));
  });

const outboxCount = (page: Page) =>
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

// Chrome does not flip navigator.onLine for Playwright's setOffline, and the app (like a real browser) goes offline-first on
// it. This init script makes onLine follow a localStorage flag, which survives the reload the spec does while offline.
async function emulateOnLine(page: Page) {
  await page.addInitScript(() => {
    Object.defineProperty(Navigator.prototype, "onLine", { configurable: true, get: () => localStorage.getItem("mm-e2e-offline") !== "1" });
  });
}
async function setBrowserOnLine(page: Page, online: boolean) {
  await page.evaluate((on) => {
    if (on) localStorage.removeItem("mm-e2e-offline");
    else localStorage.setItem("mm-e2e-offline", "1");
    window.dispatchEvent(new Event(on ? "online" : "offline"));
  }, online);
}

async function goOffline(context: BrowserContext, page: Page) {
  await setBrowserOnLine(page, false);
  await context.setOffline(true);
  // the worker's own fetches must fail too
  await context.route("**/*", (r) => (r.request().serviceWorker() ? r.abort("internetdisconnected") : r.fallback()));
}
async function goOnline(context: BrowserContext, page: Page) {
  await context.unrouteAll({ behavior: "ignoreErrors" });
  await context.setOffline(false);
  await setBrowserOnLine(page, true);
}

async function syncAll(page: Page, expected: number) {
  await page.goto("/offline");
  await expect.poll(() => controlled(page), { timeout: 15_000 }).toBe(true);
  await page.getByTestId("offline-sync").click();
  await expect(page.getByTestId("offline-row")).toHaveCount(expected);
  await expect(page.locator('[data-testid="offline-row"][data-status="complete"]')).toHaveCount(expected, { timeout: 60_000 });
}

test("offline set plays offline", async ({ page, context, request }) => {
  await spyPersist(page);
  await emulateOnLine(page);
  const set = await getSet(request);
  expect(set.items).toHaveLength(2);
  const timed = [...set.items].sort((a, b) => b.duration_s - a.duration_s)[0]!;

  await page.goto("/offline");
  await expect.poll(() => controlled(page), { timeout: 15_000 }).toBe(true);
  await expect(page.getByTestId("offline-row")).toHaveCount(2);
  await expect(page.locator('[data-testid="offline-row"][data-status="none"]')).toHaveCount(2);

  await page.getByTestId("offline-sync").click();
  await expect(page.locator('[data-testid="offline-row"][data-status="complete"]')).toHaveCount(2, { timeout: 60_000 });
  await expect(page.getByTestId("offline-row").filter({ hasText: "Downloaded" })).toHaveCount(2);
  expect(await page.evaluate(() => (window as unknown as { __persist: number }).__persist)).toBe(1);

  const facts = await storeFacts(page);
  expect(facts).toHaveLength(2);
  for (const f of facts) {
    expect(f.complete).toBe(true);
    expect(f.chunks).toBe(Math.ceil(f.bytes / CHUNK));
    expect(f.cached).toBe(Math.ceil(f.bytes / CHUNK));
  }

  // from here on the network is gone, the worker's too
  await goOffline(context, page);
  const requests: string[] = [];
  const apiOk: string[] = [];
  const media: { fromWorker: boolean; status: number; length: number; path: string }[] = [];
  page.on("request", (r) => requests.push(new URL(r.url()).pathname));
  page.on("response", (r) => {
    const p = new URL(r.url()).pathname;
    if (p.startsWith("/api/") && r.ok()) apiOk.push(p);
    if (/^\/media\/[0-9a-f]{64}\.(flac|mp3)$/.test(p)) {
      media.push({ fromWorker: r.fromServiceWorker(), status: r.status(), length: Number(r.headers()["content-length"] ?? "-1"), path: p });
    }
  });

  let flushed: Promise<unknown> | undefined;
  try {
    await page.reload();
    await expect(page.getByTestId("offline-banner")).toBeVisible();
    const rows = page.getByTestId("offline-row");
    await expect(rows).toHaveCount(2);
    await expect(page.locator('[data-testid="offline-row"][data-status="complete"]')).toHaveCount(2);

    await rows.filter({ hasText: timed.title }).getByRole("button", { name: "Play" }).click();
    await expect.poll(() => audioTime(page), { timeout: 5000 }).toBeGreaterThanOrEqual(0.5);
    await page.evaluate(() => {
      const a = document.querySelector("audio");
      if (a) a.currentTime = 6;
    });
    await expect.poll(() => audioTime(page), { timeout: 5000 }).toBeGreaterThanOrEqual(6);

    // pause: the closed listen segment is queued in the M2a outbox
    await page.evaluate(() => document.querySelector("audio")?.pause());
    await expect.poll(() => outboxCount(page), { timeout: 10_000 }).toBeGreaterThan(0);
    let last = -1;
    await expect
      .poll(
        async () => {
          const n = await outboxCount(page);
          const settled = n === last;
          last = n;
          return settled;
        },
        { intervals: [400], timeout: 10_000 },
      )
      .toBe(true);

    expect(media.length).toBeGreaterThan(0);
    for (const m of media) {
      expect(m.fromWorker, m.path).toBe(true);
      expect(m.status, m.path).toBe(206);
      expect(m.length, m.path).toBeLessThanOrEqual(CAP);
    }
    const forbidden = requests.filter((p) => p === "/api/offline-set" || /^\/api\/tracks\/[^/]+(\/media)?$/.test(p));
    expect(forbidden).toEqual([]);
    expect(apiOk).toEqual([]);
  } finally {
    // the `online` event flushes the outbox at once, so the response is awaited from before the network returns
    flushed = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/listen-events" && r.ok(), { timeout: 30_000 });
    flushed.catch(() => {});
    await goOnline(context, page);
  }

  // back online the outbox drains through log_listen_events
  await flushed;
  await expect.poll(() => outboxCount(page), { timeout: 20_000 }).toBe(0);
});

test("worker serves cached ranges capped at 4 MiB", async ({ page }) => {
  await page.goto("/offline");
  await expect.poll(() => controlled(page), { timeout: 15_000 }).toBe(true);
  const sha = "ab".repeat(32);
  const total = 9 * 1024 * 1024;

  // a synthetic 9 MiB file written into the C115.2 store: chunks in Cache Storage, metadata in IndexedDB
  await page.evaluate(
    async ({ sha, total, chunk }) => {
      const cache = await caches.open("mm-media-v1");
      const n = Math.ceil(total / chunk);
      for (let i = 0; i < n; i++) {
        const len = Math.min(chunk, total - i * chunk);
        const data = new Uint8Array(len);
        for (let j = 0; j < len; j++) data[j] = (i * chunk + j) % 251;
        await cache.put(`/__offline/${sha}.flac/${i}`, new Response(data));
      }
      const db = await new Promise<IDBDatabase>((res, rej) => {
        const o = indexedDB.open("mm-offline", 1);
        o.onupgradeneeded = () => {
          o.result.createObjectStore("files");
          o.result.createObjectStore("tracks", { keyPath: "track_id" });
        };
        o.onsuccess = () => res(o.result);
        o.onerror = () => rej(o.error);
      });
      await new Promise<void>((res, rej) => {
        const tx = db.transaction("files", "readwrite");
        tx.objectStore("files").put(
          { sha, ext: "flac", content_type: "audio/flac", bytes: total, chunks: n, complete: true, track_id: "synthetic-range-track", title: "synthetic", album: "synthetic", duration_s: 1, cover_key: null, downloaded_at: Date.now() },
          `${sha}.flac`,
        );
        tx.oncomplete = () => res();
        tx.onerror = () => rej(tx.error);
      });
      db.close();
    },
    { sha, total, chunk: CHUNK },
  );

  const get = (range: string) =>
    page.evaluate(
      async ({ sha, range }) => {
        const r = await fetch(`/media/${sha}.flac`, { headers: { Range: range } });
        const b = new Uint8Array(await r.arrayBuffer());
        return { status: r.status, length: r.headers.get("content-length"), range: r.headers.get("content-range"), size: b.length, first: b[0], last: b[b.length - 1] };
      },
      { sha, range },
    );

  try {
    const head = await get("bytes=0-");
    expect(head.status).toBe(206);
    expect(head.length).toBe(String(CAP));
    expect(head.range).toBe(`bytes 0-${CAP - 1}/${total}`);
    expect(head.size).toBe(CAP);
    expect(head.first).toBe(0);

    const tail = await get("bytes=-100");
    expect(tail.status).toBe(206);
    expect(tail.size).toBe(100);
    expect(tail.range).toBe(`bytes ${total - 100}-${total - 1}/${total}`);
    expect(tail.last).toBe((total - 1) % 251);

    expect((await get(`bytes=${total}-`)).status).toBe(416);
  } finally {
    await page.evaluate(async (sha) => {
      await (await caches.open("mm-media-v1")).keys().then(async (ks) => {
        const c = await caches.open("mm-media-v1");
        for (const k of ks) if (new URL(k.url).pathname.startsWith(`/__offline/${sha}.flac/`)) await c.delete(k);
      });
      const db = await new Promise<IDBDatabase>((res, rej) => {
        const o = indexedDB.open("mm-offline", 1);
        o.onsuccess = () => res(o.result);
        o.onerror = () => rej(o.error);
      });
      await new Promise<void>((res) => {
        const tx = db.transaction("files", "readwrite");
        tx.objectStore("files").delete(`${sha}.flac`);
        tx.oncomplete = () => res();
      });
      db.close();
    }, sha);
  }
});

test("agent-edited set syncs", async ({ page, playwright, request, baseURL }) => {
  const token = process.env.MM_E2E_AGENT_TOKEN;
  if (!token) throw new Error("the e2e agent token is created by global setup");
  const agent = await playwright.request.newContext({ baseURL: baseURL ?? "", extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
  const original = await getSet(request);
  const have = new Set(original.items.map((i) => i.track_id));
  const third = (await allTracks(request)).find((t) => t.title.startsWith("Syn song") && !have.has(t.id));
  if (!third) throw new Error("the e2e profile has more than two Syn song tracks");

  const patch = async (change: Record<string, unknown>) => {
    const cur = await apiJSON<OfflineSet>(agent, "GET", "/api/offline-set");
    await apiJSON(agent, "PATCH", `/api/playlists/${cur.playlist_id}`, { revision: cur.revision, ...change });
  };

  try {
    await syncAll(page, 2);
    await patch({ add_track_ids: [third.id] });
    await page.getByTestId("offline-sync").click();
    await expect(page.getByTestId("offline-row")).toHaveCount(3);
    await expect(page.locator('[data-testid="offline-row"][data-status="complete"]')).toHaveCount(3, { timeout: 60_000 });
    await expect(page.getByTestId("offline-row").filter({ hasText: third.title })).toHaveCount(1);

    await patch({ track_ids: original.items.map((i) => i.track_id) });
    await page.getByTestId("offline-sync").click();
    await expect(page.getByTestId("offline-row")).toHaveCount(2);
    await expect(page.getByTestId("offline-row").filter({ hasText: third.title })).toHaveCount(0);
    const facts = await storeFacts(page);
    expect(facts).toHaveLength(2);
  } finally {
    const cur = await getSet(request);
    if (cur.items.length !== original.items.length) await patch({ track_ids: original.items.map((i) => i.track_id) });
    await agent.dispose();
  }
});

test("api is never served by the worker", async ({ page, context }) => {
  await emulateOnLine(page);
  await page.goto("/offline");
  await expect.poll(() => controlled(page), { timeout: 15_000 }).toBe(true);
  await page.evaluate(() => {
    (window as unknown as { __alive: boolean }).__alive = true;
  });
  await goOffline(context, page);
  try {
    const outcome = await page.evaluate(() =>
      fetch("/api/ping").then(
        (r) => `status ${r.status}`,
        (e: unknown) => (e instanceof TypeError ? "network error" : String(e)),
      ),
    );
    expect(outcome).toBe("network error");
    // no session banner reload: the document is the same one
    await page.waitForTimeout(500);
    expect(await page.evaluate(() => (window as unknown as { __alive?: boolean }).__alive === true)).toBe(true);
  } finally {
    await goOnline(context, page);
  }
});
