// matrix: @mobile because the Song page (take cards, Pick, duel start) is the phone listening surface
import { expect as baseExpect, test, type APIRequestContext, type Page, type PlaywrightWorkerArgs } from "./fixtures";
import { apiJSON, idemKey, uniq } from "./helpers";
import { archiveDrafts, composeSong, getSong, makeDraftSongs, type ComposedSong, type SongDetail } from "./l1Api";

// Each worker server composes ONE Song of three takes (a root and two new-seed children
// through the human estimate/confirm path, fake backend; every take spends against the shared daily cap) and the tests
// of the file run in order on it, so ratings, pins, duels and proposals never touch another spec's Song.

// Songs are derived and the single writer is shared with the heavier tests of this server (52 drafts), so waits are generous.
const expect = baseExpect.configure({ timeout: 20_000 });

test.describe.configure({ mode: "serial", timeout: 180_000 });

let composed: Promise<ComposedSong> | undefined;
/** The file's Song (made by the first test that needs it, per worker). */
const theSong = (request: APIRequestContext) => (composed ??= composeSong(request, "l1s", 3));

const card = (page: Page, takeId: string) => page.locator(`[data-testid="take-card"][data-take-id="${takeId}"]`);

/** A take of the Song that is not its current Pick. */
function nonPick(d: SongDetail, last = false): string {
  const others = d.takes.filter((x) => !x.is_pick);
  const t = last ? others[others.length - 1] : others[0];
  if (!t) throw new Error("a Song with three takes has a take that is not the Pick");
  return t.id;
}

test("a rendered Song shows its cards, a rated take can be pinned and unpinned", async ({ page, request }) => {
  const s = await theSong(request);
  await page.goto(`/songs/${s.songId}`);
  await expect(page.getByTestId("take-card")).toHaveCount(3);
  await expect(page.getByTestId("song-state")).not.toHaveText("draft");
  await expect(page.getByTestId("pick-badge")).toHaveCount(1);

  const before = await getSong(request, s.songId);
  const target = nonPick(before);
  const c = card(page, target);
  await expect(c.getByTestId("pick-badge")).toHaveCount(0);

  // rate: the Song page refetches shortly after, and the pending flag clears
  await c.getByRole("button", { name: "4 stars" }).click();
  await expect(c.getByRole("button", { name: "4 stars" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByTestId("song-pending")).toHaveCount(0, { timeout: 15_000 });

  // pin: the Pick badge moves to the pinned card and the history records it
  await c.getByTestId("pin").click();
  await expect(c.getByTestId("pick-badge")).toHaveCount(1);
  await expect(c.getByTestId("pick-badge")).toContainText("Pinned");
  await expect(page.getByTestId("pick-badge")).toHaveCount(1);
  await expect(page.getByTestId("pick-event").filter({ hasText: /·\s*pinned\s*$/ }).first()).toBeVisible();
  expect((await getSong(request, s.songId)).pick.pinned_take_id).toBe(target);

  // unpin: the pin control comes back and the history says so
  await c.getByTestId("unpin").click();
  await expect(c.getByTestId("pin")).toBeVisible();
  await expect(page.getByTestId("pick-event").filter({ hasText: "unpinned" }).first()).toBeVisible();
  expect((await getSong(request, s.songId)).pick.pinned_take_id).toBeNull();

  // the full history (list_pick_events) holds the pin and the unpin
  await page.getByTestId("pick-history-all").click();
  await expect(page.getByTestId("pick-history-all")).toHaveCount(0);
  const events = page.getByTestId("pick-event");
  expect(await events.count()).toBeGreaterThanOrEqual(2);
});

test("two cards start a blind duel", async ({ page, request }) => {
  const s = await theSong(request);
  await page.goto(`/songs/${s.songId}`);
  await expect(page.getByTestId("take-card")).toHaveCount(3);
  const start = page.getByTestId("duel-start");
  await expect(start).toBeDisabled();
  const sel = page.getByTestId("duel-select");
  await sel.nth(0).click();
  await sel.nth(1).click();
  await expect(start).toBeEnabled();
  await start.click();
  await expect(page).toHaveURL(/\/duels\/[0-9a-f-]{36}$/);
  const id = page.url().split("/").pop() ?? "";
  const duel = await apiJSON<{ blind: boolean }>(request, "GET", `/api/duels/${id}`);
  expect(duel.blind).toBe(true);
});

async function propose(playwright: PlaywrightWorkerArgs["playwright"], baseURL: string | undefined, token: string, songId: string, takeId: string, rationale: string): Promise<void> {
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
  try {
    const r = await agent.post(`/api/songs/${songId}/pick-proposals`, { data: { take_id: takeId, rationale }, headers: { "Idempotency-Key": idemKey() } });
    expect(r.status(), await r.text()).toBeLessThan(300);
  } finally {
    await agent.dispose();
  }
}

test("a dismissed proposal leaves the Pick alone", async ({ page, request, playwright, baseURL, agentToken }) => {
  const s = await theSong(request);
  const before = await getSong(request, s.songId);
  await propose(playwright, baseURL, agentToken, s.songId, nonPick(before), "e2e agent: maybe this one");

  await page.goto(`/songs/${s.songId}`);
  await expect(page.getByTestId("proposal")).toHaveCount(1);
  await page.getByTestId("proposal-dismiss").click();
  await expect(page.getByTestId("proposal")).toHaveCount(0);
  const after = await getSong(request, s.songId);
  expect(after.pick.pinned_take_id).toBeNull();
  expect(after.pick.take_id).toBe(before.pick.take_id);
  expect(after.proposals.filter((p) => p.state === "open")).toHaveLength(0);
});

test("an agent proposal appears on the Song and Accept pins it", async ({ page, request, playwright, baseURL, agentToken }) => {
  const s = await theSong(request);
  const target = nonPick(await getSong(request, s.songId), true);
  await propose(playwright, baseURL, agentToken, s.songId, target, "e2e agent: this take is stronger");

  await page.goto(`/songs/${s.songId}`);
  const prop = page.getByTestId("proposal");
  await expect(prop).toHaveCount(1);
  await expect(prop).toContainText("e2e agent: this take is stronger");
  await prop.getByTestId("proposal-accept").click();
  await expect(card(page, target).getByTestId("pick-badge")).toContainText("Pinned");
  await expect(page.getByTestId("proposal")).toHaveCount(0);
  expect((await getSong(request, s.songId)).pick.pinned_take_id).toBe(target);
});

test("the Song title and angle can be edited from the page", async ({ page, request }) => {
  // a draft Song (free): editing needs no takes
  const [d] = await makeDraftSongs(request, 1, uniq("l1s").toLowerCase(), false);
  if (!d) throw new Error("one draft Song expected");
  const s = { songId: d.songId };
  await page.goto(`/songs/${s.songId}`);
  await page.getByTestId("song-edit").click();
  await page.getByTestId("song-title-input").fill("Edited e2e song");
  await page.getByTestId("song-angle-input").fill("gamma");
  await page.getByTestId("song-save").click();
  await expect(page.getByTestId("song-title")).toHaveText("Edited e2e song");
  await expect(page.getByTestId("song-angle")).toHaveText("gamma");
  expect((await getSong(request, s.songId)).song.title).toBe("Edited e2e song");
  await archiveDrafts(request, [d.draftId]);
});
