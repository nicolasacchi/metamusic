// matrix: @mobile because the Sounds page is browsed from the phone (facet chips and the Song list)
import { expect as baseExpect, test } from "./fixtures";
import { apiJSON } from "./helpers";
import { composeSong, letters, waitSongs } from "./l1Api";

// Layout and data only. The mapping test composes its own Song with a unique, letters-only
// style term, so no other spec's Song or total moves.

// Songs are derived and the single writer is shared with the heavier tests of this server (52 drafts), so waits are generous.
const expect = baseExpect.configure({ timeout: 20_000 });

test.describe.configure({ timeout: 180_000 });

interface Search {
  total: number;
  songs: { id: string }[];
  facets: Record<string, { value: string; count: number }[] | null>;
}

test("facet counts drill down to the matching Songs", async ({ page, request }) => {
  await page.goto("/sounds");
  const values = page.getByTestId("facet-value");
  await expect(values.first()).toBeVisible();
  // the list reorders while other specs add Songs: read the identity in ONE call, then pin the element by it
  const [facet, value] = await values.first().evaluate((e) => [e.getAttribute("data-facet") ?? "", e.getAttribute("data-value") ?? ""]);
  const first = page.locator(`[data-testid="facet-value"][data-facet="${facet}"][data-value="${value}"]`);
  const count = Number((await first.getByTestId("facet-count").innerText()).trim());
  expect(count).toBeGreaterThan(0);

  const query = `/api/sounds/search?sound=${encodeURIComponent(`${facet}:${value}`)}&limit=1`;
  // other specs of this server add AND hide Songs while this one runs, so the total is not monotonic: the shown total
  // must lie within the range of the API totals observed from before the click until the poll succeeds
  const seen = [(await apiJSON<Search>(request, "GET", query)).total];
  await first.click();
  await expect(page).toHaveURL(new RegExp(`sound=${facet}`));
  await expect(page.getByTestId("selected-pill")).toHaveCount(1);
  // the previous list's total stays until the filtered one arrives, hence the poll
  await expect
    .poll(async () => {
      const shown = Number((await page.getByTestId("songs-total").innerText()).trim());
      seen.push((await apiJSON<Search>(request, "GET", query)).total);
      return shown >= Math.min(...seen) && shown <= Math.max(...seen);
    })
    .toBe(true);
  await expect(page.getByTestId("sound-song").first()).toBeVisible();

  await page.getByTestId("clear-sounds").click();
  await expect(page.getByTestId("selected-pill")).toHaveCount(0);
  await expect(page).not.toHaveURL(/sound=/);
});

test("the tempo mismatch toggle narrows the list to Songs whose tempo disagrees", async ({ page, request }) => {
  await page.goto("/sounds");
  await expect(page.getByTestId("facet-value").first()).toBeVisible();
  const toggle = page.getByTestId("tempo-mismatch-toggle");
  await toggle.check();
  await expect(page).toHaveURL(/tempo_mismatch=true/);
  await expect(page.getByTestId("songs-total")).not.toHaveText("");
  const shown = Number((await page.getByTestId("songs-total").innerText()).trim());
  const mismatch = await apiJSON<Search>(request, "GET", "/api/sounds/search?tempo_mismatch=true&limit=1");
  const all = await apiJSON<Search>(request, "GET", "/api/sounds/search?limit=1");
  expect(shown).toBeLessThanOrEqual(all.total);
  expect(mismatch.total).toBeLessThanOrEqual(all.total);
  await toggle.uncheck();
  await expect(page).not.toHaveURL(/tempo_mismatch/);
});

test("mapping an unmapped term puts the Song under the facet", async ({ page, request }) => {
  const term = letters("zq");
  const value = letters("mood");
  const s = await composeSong(request, "l1x", 1, `english, synth pop, 100 bpm, ${term}`);
  await waitSongs(request, `track_id=${s.root}`);

  await page.goto("/sounds?tab=terms");
  const row = page.getByTestId("unmapped-term").filter({ hasText: term });
  await expect(row).toHaveCount(1, { timeout: 30_000 });
  await row.getByTestId("map-open").click();
  await row.getByTestId("map-facet").selectOption("mood");
  await row.getByTestId("map-value").fill(value);
  await row.getByTestId("map-submit").click();
  await expect(page.getByTestId("sounds-note")).toContainText(`Mapped "${term}"`);
  await expect(page.getByTestId("unmapped-term").filter({ hasText: term })).toHaveCount(0);
  await page.getByTestId("taxonomy-q").fill(term);
  await expect(page.getByTestId("mapping-row").filter({ hasText: term })).toHaveCount(1);

  await page.goto(`/sounds?sound=${encodeURIComponent(`mood:${value}`)}`);
  const songs = page.getByTestId("sound-song");
  await expect(songs).toHaveCount(1);
  await expect(songs.getByTestId("song-chip")).toHaveAttribute("data-song-id", s.songId);
  await expect(page.getByTestId("songs-total")).toHaveText("1");
});
