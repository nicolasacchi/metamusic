// matrix: @firefox because the tape is one long FLAC played through the single audio element (decode, seek by chapter and the signed media URL differ per engine)
import { expect as baseExpect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON, uniq } from "./helpers";
import { getRecord, publishedRecord, retireRecord, titledSong, type RecordOut, type TapeOut } from "./l2Api";

// Two one-take Songs and a published Record of them (made by the first test of the worker); the e2e rig exports tapes with
// the fake pipeline. The Record is unpublished and hidden at the end.
const expect = baseExpect.configure({ timeout: 30_000 });
test.describe.configure({ mode: "serial", timeout: 240_000 });

let rec: Promise<RecordOut> | undefined;
const record = (request: APIRequestContext) =>
  (rec ??= (async () => {
    const a = await titledSong(request, "l2ta", 1);
    const b = await titledSong(request, "l2tb", 1);
    return publishedRecord(request, uniq("l2 tape"), [a.songId, b.songId]);
  })());

/** The Record's ready tape in full (get_record lists tapes without their chapters). */
async function readyTape(request: APIRequestContext, recordId: string): Promise<TapeOut> {
  const listed = (await getRecord(request, recordId)).tapes?.find((x) => x.state === "ready");
  if (!listed) throw new Error("the Record has no ready tape");
  return apiJSON<TapeOut>(request, "GET", `/api/tapes/${listed.id}`);
}

const audio = (page: Page) => page.evaluate(() => {
  const a = document.querySelector("audio");
  return { t: a?.currentTime ?? -1, paused: a?.paused ?? true, src: a?.currentSrc ?? "" };
});

test("export a tape from a published Record, poll to ready, chapters and the signed download render", async ({ page, request }) => {
  const r = await record(request);
  await page.goto(`/records/${r.record.id}`);
  await page.getByTestId("record-tab-tapes").click();
  await page.getByTestId("tape-export").click();
  await expect(page.getByTestId("tape-progress")).toBeVisible();
  const tapes = page.getByTestId("tape-chapters");
  await expect(tapes).toBeVisible({ timeout: 90_000 });
  await expect(page.getByTestId("tape-progress")).toHaveAttribute("data-state", "ready");
  await expect(page.getByTestId("chapter-row")).toHaveCount(2);

  const t = await readyTape(request, r.record.id);
  expect(t.chapters).toHaveLength(2);
  // the lossy banner appears exactly when the tape says it holds lossy sources
  await expect(page.getByTestId("tape-lossy")).toHaveCount(t.contains_lossy ? 1 : 0);
  const href = await page.getByTestId("tape-download").getAttribute("href");
  expect(href).toMatch(/^\/media\/.+\.flac(\?.*)?$/);
  const dl = await request.get(href!);
  expect(dl.ok()).toBe(true);
  expect((await dl.body()).subarray(0, 4).toString("latin1")).toBe("fLaC");
});

test("Play as tape enqueues the tape item in the single player and a chapter seeks", async ({ page, request }) => {
  const r = await record(request);
  const t = await readyTape(request, r.record.id);
  await page.goto(`/records/${r.record.id}`);
  await page.getByTestId("record-tab-tapes").click();
  await page.getByTestId("tape-play").click();
  await expect(page.getByTestId("player-bar")).toBeVisible();
  await expect.poll(async () => (await audio(page)).src, { timeout: 30_000 }).toMatch(/\.flac/);
  // the tape is not a take: the bar offers no rating or take link for it
  await expect(page.getByTestId("player-judge")).toHaveCount(0);
  await expect(page.getByTestId("player-bar").locator('a[href^="/tracks/"]')).toHaveCount(0);
  expect(await page.locator("audio").count()).toBe(1);

  const second = t.chapters!.find((c) => c.index === 2)!;
  await page.getByTestId("chapter-row").nth(1).click();
  await expect.poll(async () => (await audio(page)).t * 1000, { timeout: 15_000 }).toBeGreaterThanOrEqual(second.start_ms - 500);
  await expect(page.getByTestId("chapter-row").nth(1)).toHaveAttribute("aria-current", "true");
});

test("reset: the Record is retired", async ({ request }) => {
  const r = await record(request);
  await retireRecord(request, r.record.id);
  expect(((await apiJSON<RecordOut>(request, "GET", `/api/records/${r.record.id}`)).record.hidden)).toBe(true);
});
