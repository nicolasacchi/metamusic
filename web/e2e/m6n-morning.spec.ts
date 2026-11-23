// matrix: chrome-only because the headset grammar is driven through the Media Session of one desktop Chrome; the phone layout and the real headset are the device checklist (DC-M6N-3)
// M6N-W2 (m6-night C384): the Morning Crate pages against the real server with the fake backend. Nothing is stubbed: the seven
// Morning Crate operations are answered by the Go handlers and counted by the served check. The headset is simulated at the one
// place the browser exposes it, the Media Session action handlers (an init script keeps them), exactly what a lock screen calls.
import { expect as baseExpect, test, type Page } from "./fixtures";
import { allTracks, apiJSON } from "./helpers";
import { composeRoots } from "./m3Api";

const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 240_000 });

interface Chapter {
  n: number;
  side: number;
  start_ms: number;
  end_ms: number;
  verdict: string;
  stars: number | null;
  strong_markers: number;
  markers: number;
  track: { id: string; title: string } | null;
}
interface Crate {
  id: string;
  state: string;
  revealed: boolean;
  chapters: Chapter[];
  playlist_id: string | null;
}

interface Posted {
  crate: string;
  kinds: string[];
  status: number;
}

/** What the page sent to submit_morning_crate_events, and what the server answered. */
function watchEvents(page: Page): Posted[] {
  const out: Posted[] = [];
  page.on("response", (res) => {
    const m = /\/api\/morning-crates\/([^/]+)\/events/.exec(res.url());
    if (!m || res.request().method() !== "POST") return;
    let kinds: string[] = [];
    try {
      kinds = (JSON.parse(res.request().postData() ?? "{}") as { events: { kind: string }[] }).events.map((e) => e.kind);
    } catch {
      /* a body that is not JSON shows as no kinds */
    }
    out.push({ crate: m[1] ?? "", kinds, status: res.status() });
  });
  return out;
}

const sent = (posted: Posted[], kind: string) => posted.filter((p) => p.status === 200 && p.kinds.includes(kind)).length;

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { __ms: Record<string, (() => void) | null> };
    w.__ms = {};
    const ms = navigator.mediaSession;
    if (!ms) return;
    const orig = ms.setActionHandler.bind(ms);
    ms.setActionHandler = (a, h) => {
      w.__ms[a] = h as (() => void) | null;
      orig(a, h);
    };
  });
});

const press = (page: Page, action: "previoustrack" | "nexttrack") =>
  page.evaluate((a) => {
    const h = (window as unknown as { __ms: Record<string, (() => void) | null> }).__ms[a];
    if (!h) throw new Error(`no ${a} handler registered`);
    h();
  }, action);

const lock = (page: Page) =>
  page.evaluate(() => {
    const m = navigator.mediaSession.metadata;
    return m ? { title: m.title, artist: m.artist, album: m.album, art: m.artwork.length } : null;
  });

const audio = (page: Page) =>
  page.evaluate(() => {
    const a = document.querySelector("audio");
    return { t: a?.currentTime ?? -1, d: a?.duration ?? NaN, paused: a?.paused ?? true, src: a?.currentSrc ?? "" };
  });

const getCrate = (request: Parameters<typeof apiJSON>[0], id: string) => apiJSON<Crate>(request, "GET", `/api/morning-crates/${id}`);

let crateId = "";
let secondId = "";
let rootIds: string[] = [];

test("the settings are read and saved, and the daily build is switched off for this test", async ({ page }) => {
  await page.goto("/morning");
  await expect(page.getByRole("heading", { level: 1, name: "Morning crates" })).toBeVisible();
  await expect(page.getByTestId("morning-settings")).toBeVisible();
  await expect(page.getByTestId("morning-at")).toHaveValue("07:00");
  await page.getByTestId("morning-at").fill("25:00");
  await page.getByTestId("morning-settings-save").click();
  await expect(page.getByTestId("morning-settings-error")).toContainText("HH:MM");
  await page.getByTestId("morning-at").fill("07:00");
  await page.getByTestId("morning-set-minutes").fill("30");
  await page.getByTestId("morning-enabled").uncheck(); // a scheduled build at the wrong moment must not take this test's takes
  await page.getByTestId("morning-settings-save").click();
  await expect(page.getByTestId("morning-settings-error")).toHaveCount(0);
  await page.reload();
  await expect(page.getByTestId("morning-set-minutes")).toHaveValue("30");
  await expect(page.getByTestId("morning-enabled")).not.toBeChecked();
});

test("a Morning Crate is built from fresh takes with the button, and is blind", async ({ page, request }) => {
  rootIds = await composeRoots(request, 3, "mc");
  await page.goto("/morning");
  await page.getByTestId("morning-build").click();
  const row = page.getByTestId("morning-row").first();
  await expect(row).toHaveAttribute("data-state", "ready", { timeout: 90_000 });
  await row.getByRole("link").click();
  await expect(page).toHaveURL(/\/morning\/[^/]+$/);
  crateId = page.url().split("/").pop() ?? "";
  const c = await getCrate(request, crateId);
  expect(c.revealed).toBe(false);
  expect(c.chapters.length).toBeGreaterThanOrEqual(3);
  await expect(page.getByTestId("morning-chapter")).toHaveCount(c.chapters.length);
  // blind: no title of any take is on the page, and the server sent none
  const body = (await page.getByTestId("morning-crate-page").innerText()).toLowerCase();
  for (const t of await allTracks(request)) expect(body).not.toContain(t.title.toLowerCase());
  expect(c.chapters.every((x) => x.track === null)).toBe(true);
  await expect(page.getByTestId("morning-unrated")).toContainText(`${c.chapters.length} left`);
  await expect(page.getByTestId("morning-reveal")).toHaveCount(0);
});

test("the headset: next skips, previous twice keeps, the lock screen stays blind", async ({ page, request }) => {
  const posted = watchEvents(page);
  await page.goto(`/morning/${crateId}`);
  const c = await getCrate(request, crateId);
  const n = c.chapters.length;
  await page.getByTestId("morning-side-0").click();
  await expect.poll(async () => (await audio(page)).src).toMatch(/\/media\/[0-9a-f]{64}\.flac\?/);
  await expect.poll(async () => (await audio(page)).t, { timeout: 15_000 }).toBeGreaterThan(0.2);
  await expect.poll(async () => (await lock(page))?.title).toBe(`Take 1 of ${n}`);
  const meta = await lock(page);
  expect(meta?.artist).toBe("Morning crate");
  expect(meta?.album).toBe("Side A");
  expect(meta?.art).toBeGreaterThan(0);
  const body = (await page.locator("body").innerText()).toLowerCase();
  for (const t of await allTracks(request)) expect(body).not.toContain(t.title.toLowerCase());

  // nexttrack: a skip event and a seek to the second chapter's slate
  await press(page, "nexttrack");
  await expect.poll(() => sent(posted, "skip"), { timeout: 15_000 }).toBeGreaterThan(0);
  const second = c.chapters.find((x) => x.n === 2)!;
  await expect.poll(async () => (await audio(page)).t, { timeout: 10_000 }).toBeGreaterThanOrEqual(second.start_ms / 1000 - 0.3);
  await expect.poll(async () => (await lock(page))?.title, { timeout: 10_000 }).toBe(`Take 2 of ${n}`); // after the 2 s flash

  // two previoustrack presses inside 3 s: ONE strong marker, nothing lands on the take before the reveal
  await press(page, "previoustrack");
  await press(page, "previoustrack");
  await expect.poll(() => sent(posted, "strong_marker"), { timeout: 15_000 }).toBe(1);
  expect(posted.flatMap((p) => p.kinds).filter((k) => k === "marker")).toHaveLength(0);
  for (const t of await allTracks(request)) {
    const cs = await apiJSON<{ items: { source: string }[] }>(request, "GET", `/api/tracks/${t.id}/comments`);
    expect(cs.items.filter((x) => x.source === "headset")).toHaveLength(0);
  }
  await page.getByTestId("morning-toggle").click(); // pause
});

test("a side that plays to its end completes its last chapter", async ({ page, request }) => {
  const posted = watchEvents(page);
  await page.goto(`/morning/${crateId}`);
  await page.getByTestId("morning-side-0").click();
  await expect.poll(async () => (await audio(page)).d).toBeGreaterThan(5);
  await page.evaluate(() => {
    const a = document.querySelector("audio");
    if (a) a.currentTime = Math.max(0, a.duration - 1.5);
  });
  await expect.poll(() => sent(posted, "complete"), { timeout: 20_000 }).toBeGreaterThan(0);
  expect((await getCrate(request, crateId)).revealed).toBe(false);
});

test("stars on every chapter reveal the crate with titles, and the headset marks land on the right take", async ({ page, request }) => {
  const posted = watchEvents(page);
  await page.goto(`/morning/${crateId}`);
  const c = await getCrate(request, crateId);
  for (const ch of c.chapters) await page.getByTestId("morning-chapter").nth(ch.n - 1).getByRole("button", { name: "4 stars", exact: true }).click();
  await expect(page.getByTestId("morning-reveal")).toBeVisible({ timeout: 30_000 });
  expect(sent(posted, "rate")).toBeGreaterThan(0);
  const r = await getCrate(request, crateId);
  expect(r.revealed).toBe(true);
  expect(r.playlist_id).not.toBeNull();
  await expect(page.getByTestId("morning-reveal-row")).toHaveCount(r.chapters.length);
  for (const ch of r.chapters) await expect(page.getByTestId("morning-reveal")).toContainText(ch.track!.title);
  // chapter 1 was skipped, chapter 2 got the keep
  expect(r.chapters.find((x) => x.n === 1)!.verdict).toBe("skipped");
  const two = r.chapters.find((x) => x.n === 2)!;
  expect(two.verdict).toBe("kept");
  expect(two.strong_markers).toBe(1);
  await expect(page.getByTestId("morning-reveal-row").nth(1)).toHaveAttribute("data-verdict", "kept");
  await expect(page.getByTestId("morning-reveal-row").nth(0)).toHaveAttribute("data-verdict", "skipped");
  const cs = await apiJSON<{ items: { source: string; kind: string }[] }>(request, "GET", `/api/tracks/${two.track!.id}/comments`);
  expect(cs.items.filter((x) => x.source === "headset" && x.kind === "keep")).toHaveLength(1);
  const rating = await apiJSON<{ rating: { mine?: { stars: number } | null } }>(request, "GET", `/api/tracks/${two.track!.id}`);
  expect(JSON.stringify(rating.rating)).toContain("4");
  // titles come back on the lock screen after the reveal
  await page.getByTestId("morning-side-0").click();
  await expect.poll(async () => (await lock(page))?.title, { timeout: 15_000 }).toBe(r.chapters[0]!.track!.title);
});

test("Duel these two starts a blind duel from the revealed crate", async ({ page }) => {
  await page.goto(`/morning/${crateId}`);
  await expect(page.getByTestId("morning-duel")).toBeDisabled();
  await page.getByTestId("morning-pick").nth(0).check();
  await page.getByTestId("morning-pick").nth(1).check();
  await page.getByTestId("morning-duel").click();
  await expect(page).toHaveURL(/\/duels\/[^/]+$/);
});

test("a crate is revealed by hand, its playlist exists only after, and the list shows both", async ({ page, request }) => {
  const made = await apiJSON<Crate>(request, "POST", "/api/morning-crates", { include_track_ids: rootIds }, true);
  secondId = made.id;
  await expect.poll(async () => (await getCrate(request, secondId)).state, { timeout: 90_000 }).toBe("ready");
  expect((await getCrate(request, secondId)).playlist_id).toBeNull();
  await page.goto(`/morning/${secondId}`);
  await expect(page.getByTestId("morning-reveal")).toHaveCount(0);
  await page.getByTestId("morning-reveal-now").click();
  await expect(page.getByTestId("morning-reveal")).toBeVisible();
  await expect(page.getByTestId("morning-playlist")).toBeVisible();
  expect((await getCrate(request, secondId)).revealed).toBe(true);
  await page.goto("/morning");
  await expect(page.getByTestId("morning-row")).toHaveCount(2);
  await expect(page.getByTestId("morning-row").first()).toHaveAttribute("data-revealed", "true");
});

test("New shows the card for a crate made today, with the unrated badge", async ({ page, request }) => {
  const third = await apiJSON<Crate>(request, "POST", "/api/morning-crates", { include_track_ids: rootIds }, true);
  await expect.poll(async () => (await getCrate(request, third.id)).state, { timeout: 90_000 }).toBe("ready");
  await page.goto("/");
  const card = page.getByTestId("morning-card");
  await expect(card).toBeVisible();
  await expect(card).toHaveAttribute("href", `/morning/${third.id}`);
  await expect(page.getByTestId("morning-card-badge")).toContainText("to rate");
});

test("agents can read and build, but never submit events or reveal", async ({ playwright, baseURL, agentToken }) => {
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${agentToken}` } });
  expect((await agent.get("/api/morning-crates")).status()).toBe(200);
  const ev = await agent.post(`/api/morning-crates/${crateId}/events`, { data: { events: [{ id: "01930000-0000-7000-8000-000000000001", side: 0, chapter: 1, kind: "play", side_ms: 0, client_at: new Date().toISOString() }] } });
  expect(ev.status()).toBe(403);
  expect((await ev.json()).code).toBe("HUMAN_ONLY");
  const rv = await agent.post(`/api/morning-crates/${secondId}/reveal`);
  expect(rv.status()).toBe(403);
  await agent.dispose();
});
