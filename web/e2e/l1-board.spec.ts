// matrix: @mobile because the Board is used from the phone (layout, touch selection and the hand-off to Generate)
import { expect as baseExpect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON, uniq } from "./helpers";
import { archiveDrafts, assignSongs, createMuse, letters, makeDraftSongs, setAngle, type SongList } from "./l1Api";

// Layout and data only. Every spec makes its own Muse and draft Songs under a
// unique tag and asserts relative effects; none depends on a count another spec changes. The spec stops at the
// Generate page (it never confirms spend).

// Songs are derived and the single writer is shared with the heavier tests of this server (52 drafts), so waits are generous.
const expect = baseExpect.configure({ timeout: 20_000 });

test.describe.configure({ timeout: 120_000 });

const isPhone = (page: Page) => (page.viewportSize()?.width ?? 1280) < 768;
/** Phone: chips open the Song until the explicit Select mode is on (it also shows the action bar). */
async function enterSelect(page: Page) {
  if (isPhone(page)) await page.getByTestId("select-toggle").click();
}

interface BoardOut {
  totals: { songs: number; muses: number };
}
interface SessionList {
  items: { id: string; name: string; songs: number; revision: number }[];
}

test("the Board shows a Muse with its chips under their angles", async ({ page, request }) => {
  const tag = uniq("l1b").toLowerCase();
  const songs = await makeDraftSongs(request, 3, tag, true);
  const [a, b, c] = songs;
  if (!a || !b || !c) throw new Error("three songs expected");
  const muse = await createMuse(request, `Muse ${tag}`, `Artist ${tag}`);
  try {
    await assignSongs(request, muse.id, songs.map((s) => s.songId));
    await setAngle(request, a.songId, "alpha");
    await setAngle(request, b.songId, "alpha");
    await setAngle(request, c.songId, "beta");

    await page.goto(`/board?muse=${muse.id}`);
    const row = page.getByTestId("board-row");
    await expect(row).toHaveCount(1);
    await expect(row).toContainText(`Muse ${tag}`);
    await expect(row.getByTestId("song-chip")).toHaveCount(3);
    const cols = page.getByTestId(isPhone(page) ? "board-angle" : "board-column");
    await expect(cols.filter({ hasText: "alpha" })).toHaveCount(1);
    await expect(cols.filter({ hasText: "beta" })).toHaveCount(1);
    await expect(row.locator(`[data-song-id="${c.songId}"]`)).toBeVisible();
    await expect(page.getByTestId("board-totals")).toContainText("3 songs");
  } finally {
    await archiveDrafts(request, songs.map((s) => s.draftId));
  }
});

test("a new Muse takes two selected chips", async ({ page, request }) => {
  const tag = uniq("l1b").toLowerCase();
  const songs = await makeDraftSongs(request, 2, tag, true);
  try {
    await page.goto(`/board?q=${tag}`);
    const chips = page.getByTestId("song-chip");
    await expect(chips).toHaveCount(2);
    await enterSelect(page);
    await chips.nth(0).click();
    await chips.nth(1).click();
    await expect(page.getByTestId("selection-count")).toHaveText("2 selected");

    await page.getByTestId("assign-open").click();
    await page.getByTestId("muse-create-open").click();
    const title = `Picked ${tag}`;
    await page.getByTestId("muse-title").fill(title);
    await page.getByTestId("muse-create").click();

    // the reload (it clears the note) shows the Muse row with both chips; the API agrees
    const row = page.getByTestId("board-row").filter({ hasText: title });
    await expect(row.getByTestId("song-chip")).toHaveCount(2);
    const mine = await apiJSON<SongList>(request, "GET", `/api/songs?q=${tag}`);
    expect(mine.items.map((s) => s.muse_id).filter((m) => m !== null)).toHaveLength(2);
    expect(new Set(mine.items.map((s) => s.muse_id)).size).toBe(1);
  } finally {
    await archiveDrafts(request, songs.map((s) => s.draftId));
  }
});

// Other specs of this server create Songs (and their derived Sounds) while this one runs, so a total shown by the page and
// one read from the API at another instant can differ. The page's number must lie within the range of the API totals
// observed from before the page changed until the poll succeeds.
async function expectShownSongsWithinApi(page: Page, request: APIRequestContext, url: string, before: number): Promise<number> {
  const seen = [before];
  await expect
    .poll(async () => {
      const text = await page.getByTestId("board-totals").innerText();
      const shown = Number(/(\d+) songs/.exec(text)?.[1] ?? Number.NaN);
      seen.push((await apiJSON<BoardOut>(request, "GET", url)).totals.songs);
      return shown >= Math.min(...seen) && shown <= Math.max(...seen);
    })
    .toBe(true);
  return seen[seen.length - 1] ?? before;
}

test("the Board filters by session and by a Sound facet", async ({ page, request }) => {
  const sessions = await apiJSON<SessionList>(request, "GET", "/api/sessions?limit=100");
  const withSongs = sessions.items.find((s) => s.songs > 0);
  if (!withSongs) throw new Error("the e2e library has a session with songs");

  await page.goto("/board");
  await expect(page.getByTestId("board-totals")).toBeVisible();

  const sessionURL = `/api/board?detail=counts&limit=1&session_id=${withSongs.id}`;
  const sessionBefore = (await apiJSON<BoardOut>(request, "GET", sessionURL)).totals.songs;
  await page.getByTestId("filter-session").selectOption(withSongs.id);
  await expect(page).toHaveURL(new RegExp(`session=${withSongs.id}`));
  const bySession = await expectShownSongsWithinApi(page, request, sessionURL, sessionBefore);
  expect(bySession).toBeGreaterThan(0);

  await page.getByTestId("filter-session").selectOption("");
  await expect(page).not.toHaveURL(/session=/);

  const pill = page.getByLabel("Sound facets").locator('button[aria-pressed="false"]').first();
  await expect(pill).toBeVisible();
  const token = (await pill.getAttribute("data-sound")) ?? "";
  expect(token).toMatch(/^[a-z_]+:.+/);
  const soundURL = `/api/board?detail=counts&limit=1&sound=${encodeURIComponent(token)}`;
  const soundBefore = (await apiJSON<BoardOut>(request, "GET", soundURL)).totals.songs;
  await pill.click();
  await expect(page).toHaveURL(/sound=/);
  await expectShownSongsWithinApi(page, request, soundURL, soundBefore);
});

test("a session can be renamed from the Board and renamed back", async ({ page, request }) => {
  const sessions = await apiJSON<SessionList>(request, "GET", "/api/sessions?limit=100");
  const target = sessions.items[0];
  if (!target) throw new Error("the e2e library has a session");
  const renamed = letters("sess");

  await page.goto("/board");
  await expect(page.getByTestId("board-totals")).toBeVisible();
  const panel = page.getByTestId("session-panel");
  await panel.locator("summary").click();
  const item = panel.locator("li").filter({ hasText: target.name }).first();
  await item.getByRole("button", { name: "Rename" }).click();
  await panel.getByLabel("Session name").fill(renamed);
  await panel.getByRole("button", { name: "Save" }).click();
  const after = panel.locator("li").filter({ hasText: renamed });
  await expect(after).toHaveCount(1);
  // restore the name (the panel carries the new revision from the response)
  await after.getByRole("button", { name: "Rename" }).click();
  await panel.getByLabel("Session name").fill(target.name);
  await panel.getByRole("button", { name: "Save" }).click();
  // the row shows the old name again only once the response arrived (while editing it holds an input, not text)
  await expect(panel.locator("li").filter({ hasText: target.name }).first()).toBeVisible();
  await expect(panel.locator("li").filter({ hasText: renamed })).toHaveCount(0);
  // default batch names are minute-resolution and may repeat, so the visible row can be a sibling: wait on the API itself
  await expect
    .poll(async () => (await apiJSON<SessionList>(request, "GET", "/api/sessions?limit=100")).items.find((s) => s.id === target.id)?.name)
    .toBe(target.name);
});

test("Estimate hands off to /human/generate with the draft count", async ({ page, request }) => {
  const tag = uniq("l1b").toLowerCase();
  const songs = await makeDraftSongs(request, 3, tag, true);
  try {
    await page.goto(`/board?q=${tag}`);
    await expect(page.getByTestId("song-chip")).toHaveCount(3);
    await enterSelect(page);
    await page.getByTestId("select-drafts").click();
    await expect(page.getByTestId("selection-count")).toHaveText("3 selected");
    const estimate = page.getByTestId("estimate");
    await expect(estimate).toBeEnabled();
    await expect(estimate).toContainText("(3)");
    await estimate.click();
    await expect(page).toHaveURL(/\/human\/generate$/);
    await expect(page.getByTestId("intent-summary")).toContainText("3 drafts");
  } finally {
    await archiveDrafts(request, songs.map((s) => s.draftId));
  }
});

test("selecting all drafts stops at the 50-take cap with a note", async ({ page, request }) => {
  test.setTimeout(240_000);
  const tag = uniq("l1b").toLowerCase();
  const songs = await makeDraftSongs(request, 52, tag, false);
  const muse = await createMuse(request, `Many ${tag}`);
  try {
    await assignSongs(request, muse.id, songs.map((s) => s.songId));
    await page.goto(`/board?muse=${muse.id}`);
    await expect(page.getByTestId("board-totals")).toContainText("52 songs");
    await enterSelect(page);
    await page.getByTestId("select-drafts").click();
    await expect(page.getByTestId("selection-count")).toHaveText("50 selected");
    await expect(page.getByTestId("board-note")).toContainText("Selected the first 50 drafts");
    await expect(page.getByTestId("estimate")).toContainText("(50)");
  } finally {
    await archiveDrafts(request, songs.map((s) => s.draftId));
  }
});

test("phone: tapping a chip opens its Song; Select mode shows the sticky action bar", async ({ page, request }) => {
  test.skip(!isPhone(page), "phone layout");
  const tag = uniq("l1b").toLowerCase();
  const songs = await makeDraftSongs(request, 2, tag, true);
  try {
    await page.goto(`/board?q=${tag}`);
    await expect(page.getByTestId("board-cards")).toBeVisible();
    await expect(page.getByTestId("board-grid")).toHaveCount(0);
    await expect(page.getByTestId("estimate")).toHaveCount(0);
    const first = page.getByTestId("song-chip").first();
    const id = (await first.getAttribute("data-song-id")) ?? "";
    await first.click();
    await expect(page).toHaveURL(new RegExp(`/songs/${id}$`));
    await page.goBack();
    await page.getByTestId("select-toggle").click();
    await page.getByTestId("song-chip").first().click();
    await expect(page.getByTestId("selection-count")).toHaveText("1 selected");
    const bar = page.getByRole("toolbar", { name: "Selection actions" });
    await expect(bar).toBeVisible();
    await expect(page.getByTestId("estimate")).toBeVisible();
    await expect(page.getByTestId("assign-open")).toBeEnabled();
    const b = await bar.boundingBox();
    expect((b?.y ?? 0) + (b?.height ?? 0)).toBeLessThanOrEqual((page.viewportSize()?.height ?? 0) + 1);
  } finally {
    await archiveDrafts(request, songs.map((s) => s.draftId));
  }
});
