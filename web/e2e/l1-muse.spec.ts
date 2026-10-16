// matrix: chrome-only because the Muse page is a data and form flow with no device-dependent behaviour
import { expect as baseExpect, test } from "./fixtures";
import { apiJSON, uniq } from "./helpers";
import { archiveDrafts, assignSongs, createMuse, getMuse, makeDraftSongs } from "./l1Api";

// Layout and data only.

// Songs are derived and the single writer is shared with the heavier tests of this server (52 drafts), so waits are generous.
const expect = baseExpect.configure({ timeout: 20_000 });

test.describe.configure({ timeout: 120_000 });

test("title and artist can be edited, and a stale edit shows the conflict", async ({ page, request }) => {
  const tag = uniq("l1m").toLowerCase();
  const muse = await createMuse(request, `Before ${tag}`, `Artist ${tag}`);
  await page.goto(`/muses/${muse.id}`);
  await expect(page.getByTestId("muse-title")).toHaveText(`Before ${tag}`);

  // a plain edit
  await page.getByTestId("muse-edit").click();
  await page.getByTestId("muse-title-input").fill(`After ${tag}`);
  await page.getByTestId("muse-artist-input").fill(`Other ${tag}`);
  await page.getByTestId("muse-save").click();
  await expect(page.getByTestId("muse-title")).toHaveText(`After ${tag}`);
  await expect(page.getByTestId("muse-artist")).toHaveText(`Other ${tag}`);

  // a stale edit: somebody else saves first, then our Save carries the old revision
  await page.getByTestId("muse-edit").click();
  await page.getByTestId("muse-title-input").fill(`Stale ${tag}`);
  const cur = (await getMuse(request, muse.id)).muse;
  await apiJSON(request, "PATCH", `/api/muses/${muse.id}`, { revision: cur.revision, title: `Theirs ${tag}` });
  await page.getByTestId("muse-save").click();
  await expect(page.getByTestId("muse-note")).toContainText("Someone else changed this Muse");
  // the page reloaded the Muse; leaving the stale form shows their title
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByTestId("muse-title")).toHaveText(`Theirs ${tag}`);
  expect((await getMuse(request, muse.id)).muse.title).toBe(`Theirs ${tag}`);
});

test("hiding a Muse removes it from the Board", async ({ page, request }) => {
  const tag = uniq("l1m").toLowerCase();
  const songs = await makeDraftSongs(request, 1, tag, true);
  const muse = await createMuse(request, `Hide ${tag}`);
  try {
    await assignSongs(request, muse.id, songs.map((s) => s.songId));
    await page.goto(`/board?q=${tag}`);
    const row = page.getByTestId("board-row").filter({ hasText: `Hide ${tag}` });
    await expect(row).toHaveCount(1);

    await page.goto(`/muses/${muse.id}`);
    await page.getByTestId("muse-hide").click();
    await expect(page.getByTestId("muse-hide")).toHaveText("Unhide");
    expect((await getMuse(request, muse.id)).muse.hidden).toBe(true);

    await page.goto(`/board?q=${tag}`);
    await expect(page.getByTestId("board-totals")).toBeVisible();
    await expect(page.getByTestId("board-row").filter({ hasText: `Hide ${tag}` })).toHaveCount(0);
  } finally {
    await archiveDrafts(request, songs.map((s) => s.draftId));
  }
});
