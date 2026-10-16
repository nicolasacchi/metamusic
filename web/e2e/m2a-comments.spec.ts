// matrix: @mobile @firefox @realtime because comment-and-jump seeks the audio element (Firefox runs only the jump: tests) and the comment form is used on phones
import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON, audioTime, trackByTitle, uniq } from "./helpers";

type Comment = { id: string; at_second: number | null; body: string; hidden: boolean; state: string; link: string; section: { label: string } | null };
const list = async (request: APIRequestContext, id: string) =>
  (await apiJSON<{ items: Comment[] }>(request, "GET", `/api/tracks/${id}/comments?limit=100`)).items;
const addComment = (request: APIRequestContext, id: string, body: string, at?: number) =>
  apiJSON<Comment>(request, "POST", `/api/tracks/${id}/comments`, { body, ...(at === undefined ? {} : { at_second: at }) }, true);

const panel = (page: Page) => page.getByTestId("comments-panel");
const row = (page: Page, body: string) => page.getByTestId("comment").filter({ hasText: body });

// Only the jump tests run on Firefox (C41).
test.beforeEach(({ browserName }, info) => {
  if (browserName === "firefox") test.skip(!info.title.startsWith("jump:"), "Firefox runs the jump tests only");
});

async function compose(page: Page, body: string) {
  const c = panel(page).getByTestId("comment-composer");
  await c.getByPlaceholder("Comment").fill(body);
  await c.getByRole("button", { name: "Comment", exact: true }).click();
}

test("general comment on an untimed track has no timestamp and no section chip", async ({ page, request }) => {
  const t = await trackByTitle(request, "Syn song 04");
  const body = uniq("general");
  await page.goto(`/tracks/${t.id}`);
  await compose(page, body);
  const r = row(page, body);
  await expect(r).toBeVisible();
  await expect(r.getByTestId("comment-at")).toHaveCount(0);
  await expect.poll(async () => (await list(request, t.id)).find((c) => c.body === body)?.at_second).toBeNull();
  // a timed comment on an untimed track carries no section
  const timed = uniq("untimed-at");
  await addComment(request, t.id, timed, 2);
  await page.reload();
  const tr = row(page, timed);
  await expect(tr.getByTestId("comment-at")).toHaveText("0:02");
  await expect(tr).not.toContainText(/~\d+%/);
  expect((await list(request, t.id)).find((c) => c.body === timed)?.section).toBeNull();
});

test("comment at current time records the player position and shows the section of a timed track", async ({ page, request }) => {
  const t = await trackByTitle(request, "Syn song 03");
  const body = uniq("at-now");
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(() => audioTime(page)).toBeGreaterThan(1.5);
  await page.getByTestId("track-play").click(); // pause: the position stops moving
  await expect(page.getByTestId("track-play")).toHaveText("Play");
  const c = panel(page).getByTestId("comment-composer");
  await c.getByPlaceholder("Comment").fill(body);
  const now = await audioTime(page);
  const box = c.getByLabel(/at current time/);
  await box.check();
  await c.getByRole("button", { name: "Comment", exact: true }).click();
  const r = row(page, body);
  await expect(r.getByTestId("comment-at")).toBeVisible();
  const got = (await list(request, t.id)).find((x) => x.body === body);
  expect(got?.at_second).not.toBeNull();
  expect(Math.abs((got?.at_second ?? -9) - now)).toBeLessThanOrEqual(0.3);
  // the timed track labels the section once, at creation
  expect(got?.section?.label).toMatch(/verse|chorus|bridge|intro|outro/i);
  await expect(r).toContainText(/~\d+%/);
});

test("resolve, reopen and hide a comment", async ({ page, request }) => {
  const t = await trackByTitle(request, "Syn song 04");
  const body = uniq("lifecycle");
  const made = await addComment(request, t.id, body);
  await page.goto(`/tracks/${t.id}`);
  const r = row(page, body);
  await r.getByRole("button", { name: "Resolve" }).click();
  await expect(r).toHaveCount(0); // the default view lists open comments only
  await expect.poll(async () => (await list(request, t.id)).find((c) => c.id === made.id)?.state).toBe("resolved");
  await panel(page).getByLabel("Show").selectOption("all");
  await expect(r).toBeVisible();
  await r.getByRole("button", { name: "Reopen" }).click();
  await expect.poll(async () => (await list(request, t.id)).find((c) => c.id === made.id)?.state).toBe("open");
  await r.getByRole("button", { name: "Hide" }).click();
  await expect(r).toHaveCount(0);
  await expect.poll(async () => (await list(request, t.id)).find((c) => c.id === made.id)?.hidden ?? true).toBe(true);
});

const near = (page: Page, s: number) => expect.poll(async () => Math.abs((await audioTime(page)) - s), { timeout: 3000 }).toBeLessThanOrEqual(0.5);

test("jump: a timestamped comment seeks the paused player on the same track", async ({ page, request }) => {
  const t = await trackByTitle(request, "Syn song 03");
  const body = uniq("jump-paused");
  await addComment(request, t.id, body, 8);
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(() => audioTime(page)).toBeGreaterThan(0.5);
  await page.getByTestId("track-play").click();
  await expect(page.getByTestId("track-play")).toHaveText("Play");
  const link = row(page, body).getByTestId("comment-at");
  await expect(link).toHaveAttribute("href", /\?t=8\.0$/);
  await link.click();
  await near(page, 8);
});

test("jump: a timestamped comment seeks the playing player on the same track", async ({ page, request }) => {
  const t = await trackByTitle(request, "Syn song 03");
  const body = uniq("jump-playing");
  await addComment(request, t.id, body, 8);
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(() => audioTime(page)).toBeGreaterThan(0.5);
  await row(page, body).getByTestId("comment-at").click();
  await near(page, 8);
});

test("jump: a timestamped comment starts its track when another track is playing", async ({ page, request }) => {
  const target = await trackByTitle(request, "Syn song 03");
  const other = await trackByTitle(request, "Syn song 05");
  const body = uniq("jump-other");
  await addComment(request, target.id, body, 8);
  await page.goto(`/tracks/${other.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(() => audioTime(page)).toBeGreaterThan(0.5);
  // client-side navigation keeps the one audio element playing track 05
  await page.getByRole("link", { name: "Library" }).click();
  await page.getByTestId("track-link").filter({ hasText: "Syn song 03" }).click();
  await expect(page.getByTestId("track-title")).toHaveText("Syn song 03");
  await row(page, body).getByTestId("comment-at").click();
  await near(page, 8);
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause");
});

test("jump: a waveform pin seeks to its comment", async ({ page, request }, info) => {
  const t = await trackByTitle(request, "Syn song 03");
  const at = { chrome: 4, "chrome-pixel7": 6, "chrome-iphone15": 10, firefox: 9 }[info.project.name] ?? 5;
  await addComment(request, t.id, uniq("pin"), at);
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(() => audioTime(page)).toBeGreaterThan(0.3);
  const pin = page.getByRole("button", { name: `Comment at 0:${String(at).padStart(2, "0")}` }).first();
  await pin.scrollIntoViewIfNeeded();
  await pin.click();
  await near(page, at);
});
