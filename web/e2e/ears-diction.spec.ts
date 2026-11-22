// matrix: chrome-only because it is a server-side data flow (analyze, stored diction rows, marks, lexicon); the karaoke view is the shared Track card
import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON, audioTime, openNav } from "./helpers";
import { createHuman, estimate, pollBatch, allTerminal } from "./m3Api";
import { ownTracks } from "./helpers";

// The karaoke view and the Diction Lab (M5b W2). A take gets its listening check through the ordinary estimate and confirmation (the
// e2e human confirms), the fake worker answers it, and the diction reads (get_track_diction, get_diction_lexicon) then have data.
// Each spec file has its own server, so the take starts without a check.

test.describe.configure({ timeout: 120_000 });

interface DictionLine {
  section_idx: number;
  line_idx: number;
  start_s: number | null;
  class: string;
  class_source: string;
  mark: { human: string | null };
}

/** The take's listening check, made once per server (the specs of this file share one). */
async function analyze(request: APIRequestContext, id: string): Promise<void> {
  if ((await request.get(`/api/tracks/${id}/tier-b`)).status() === 200) return;
  const e = await estimate(request, "/api/generations/estimate", { items: [{ mode: "analyze", track_ids: [id] }] });
  const b = await createHuman(request, e, "e2e listening check");
  await pollBatch(request, b.id, allTerminal);
  await expect.poll(async () => (await request.get(`/api/tracks/${id}/tier-b`)).status(), { timeout: 60_000, intervals: [500, 1000] }).toBe(200);
}

const diction = (request: APIRequestContext, id: string) => apiJSON<{ lines: DictionLine[]; basis: string }>(request, "GET", `/api/tracks/${id}/diction`);

function watchConsole(page: Page): string[] {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  return errors;
}

test("the karaoke view lists the lines with their class, and marks are kept and withdrawn", async ({ page, request }, info) => {
  const errors = watchConsole(page);
  const [mine] = await ownTracks(request, info);
  await analyze(request, mine.id);
  const before = await diction(request, mine.id);
  expect(before.basis).toBe("asr");

  await page.goto(`/tracks/${mine.id}`);
  const karaoke = page.getByTestId("karaoke");
  await expect(karaoke).toBeVisible();
  const lines = karaoke.getByTestId("karaoke-line");
  expect(await lines.count()).toBe(before.lines.length);
  await expect(karaoke.getByTestId("karaoke-note")).toContainText("as a recogniser heard them");

  // mark a line unclear: the line is now the person's, the server keeps it
  const target = lines.nth(0);
  await target.getByTestId("mark-unclear").click();
  await expect(target).toHaveAttribute("data-source", "human");
  await expect(target).toHaveAttribute("data-class", "unclear");
  await expect(target.getByTestId("mark-unclear")).toHaveAttribute("aria-pressed", "true");
  const marked = await diction(request, mine.id);
  expect(marked.lines[0]!.mark.human).toBe("unclear");
  expect(marked.lines[0]!.class_source).toBe("human");

  // it survives a reload, and a second press withdraws it (the recogniser's class comes back)
  await page.reload();
  const again = page.getByTestId("karaoke").getByTestId("karaoke-line").nth(0);
  await expect(again).toHaveAttribute("data-source", "human");
  await again.getByTestId("mark-unclear").click();
  await expect(again).toHaveAttribute("data-source", "asr");
  await expect(again.getByTestId("mark-unclear")).toHaveAttribute("aria-pressed", "false");
  expect((await diction(request, mine.id)).lines[0]!.mark.human).not.toBe("unclear");
  expect(errors).toEqual([]);
});

// The fake worker cannot align words in these takes (they have no plan score), so no line has a time. This stubs the read with
// invented, timed Italian lines to watch the playhead in a real browser; the marks and the rest above are the server's own.
test("a tap on a timed line plays from it, the line and the word at the playhead are highlighted", async ({ page, request }, info) => {
  const errors = watchConsole(page);
  const [mine] = await ownTracks(request, info);
  await analyze(request, mine.id);
  const word = (i: number, text: string, s: number) => ({ i, text, start_s: s, end_s: s + 0.4, timing: "heard", match: "exact", heard: "" });
  const line = (idx: number, text: string, s: number, e: number) => ({
    section_idx: 0,
    line_idx: idx,
    text,
    start_s: s,
    end_s: e,
    timing: "heard",
    cer: 0.05,
    class: "clear",
    class_source: "asr",
    mark: { human: null, critic: null },
    words: text.split(" ").map((w, i) => word(i, w, s + i * 0.5)),
  });
  const body = {
    track_id: mine.id,
    basis: "asr",
    lang: "it",
    thresholds: { clear: 0.15, mixed: 0.4 },
    lexicon_hits: [],
    truncated: false,
    next_cursor: null,
    lines: [line(0, "la luna dorme", 0, 2), line(1, "il mare canta piano", 2, 4), line(2, "dormi ancora un poco", 4, 6)],
  };
  await page.route(`**/api/tracks/${mine.id}/diction`, (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) }));
  await page.goto(`/tracks/${mine.id}`);
  const karaoke = page.getByTestId("karaoke");
  await expect(karaoke.getByTestId("karaoke-line")).toHaveCount(3);
  await expect(karaoke.locator('[data-active="true"]')).toHaveCount(0); // not the playing take: no highlight
  await karaoke.getByTestId("karaoke-line").nth(1).locator("button.text").click();
  await expect(page.getByTestId("track-play")).toHaveText("Pause");
  await expect.poll(() => audioTime(page)).toBeGreaterThanOrEqual(1.9);
  await expect(karaoke.locator('[data-active="true"]')).toHaveCount(1);
  await expect(karaoke.locator(".w.now")).toHaveCount(1);
  expect(errors).toEqual([]);
});

test("a take with no listening check shows no karaoke and asks nothing the server refuses", async ({ page, request }, info) => {
  const errors = watchConsole(page);
  const [, other] = await ownTracks(request, info);
  await page.goto(`/tracks/${other.id}`);
  await expect(page.getByTestId("tierb-state")).toContainText("No listening check yet");
  await expect(page.getByTestId("karaoke")).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("the Diction Lab lists the words, filters them, and ignores and restores one", async ({ page, request }, info) => {
  const [mine] = await ownTracks(request, info);
  await analyze(request, mine.id);

  await page.goto("/more");
  await openNav(page, "Diction Lab");
  await expect(page).toHaveURL(/\/diction$/);
  await expect(page.getByRole("heading", { name: "Diction Lab" })).toBeVisible();
  const entries = page.getByTestId("lex-entry");
  await expect(entries.first()).toBeVisible();
  const total = await entries.count();
  expect(total).toBeGreaterThan(0);
  await expect(page.getByTestId("calibration")).toContainText("No lines marked yet");

  // the search narrows the list to words that contain it
  const first = entries.first();
  const word = (await first.getAttribute("data-word")) ?? "";
  expect(word.length).toBeGreaterThan(0);
  await page.getByTestId("lex-q").fill(word);
  await expect(entries.first()).toHaveAttribute("data-word", new RegExp(word.slice(0, 3)));
  await expect(page.getByTestId("lex-total")).toContainText(/\d+ words?/);

  // ignore, then restore: the status follows the server's answer
  const row = page.locator(`[data-testid="lex-entry"][data-word="${word}"]`).first();
  await row.getByTestId("lex-ignore").click();
  await expect(row).toHaveAttribute("data-status", "ignored");
  await row.getByTestId("lex-restore").click();
  await expect(row).toHaveAttribute("data-status", "active");
});
