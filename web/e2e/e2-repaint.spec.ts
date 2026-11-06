// matrix: @mobile because range selection is by touch on a phone (RangeSelect handles are 44 px), and the Track actions are laid out for a phone
import { expect, test, type Locator, type Page } from "./fixtures";
import { apiJSON, trackByTitle } from "./helpers";
import { allTerminal, first, pollBatch } from "./m3Api";
import { ACE, aceBase, ensureAdopted, runCompleted, watchE2 } from "./e2Api";

// One server per (project, spec file); the tests run in order on it. The ACE-Step source take is made once per server through
// the operations the SPA uses (compose on the engine) and is long enough for a duel window (40 s).
test.describe.configure({ mode: "serial", timeout: 150_000 });

const bases = new Map<string, string>();
async function base(request: Parameters<typeof aceBase>[0], url: string): Promise<string> {
  let id = bases.get(url);
  if (!id) {
    id = await aceBase(request, 40);
    bases.set(url, id);
  }
  return id;
}

const strip = (page: Page): Locator => page.locator('[data-testid="range-select"] .strip');
const num = async (l: Locator): Promise<number> => Number(await l.inputValue());

/** Opens "Repaint section…" on a take page and waits for the RangeSelect (list_engines is fetched by that click). */
async function openRepaint(page: Page, trackId: string): Promise<void> {
  await page.goto(`/tracks/${trackId}`);
  await expect(page.getByTestId("engine-actions")).toBeVisible();
  await page.getByTestId("action-repaint").click();
  await expect(page.getByTestId("range-select")).toBeVisible();
  await expect(page.getByTestId("action-reason")).toHaveCount(0);
}

async function setRange(page: Page, from: string, to: string): Promise<void> {
  await page.getByTestId("range-from").fill(from);
  await page.getByTestId("range-from").press("Tab"); // the change event commits the field
  await page.getByTestId("range-to").fill(to);
  await page.getByTestId("range-to").press("Tab");
}

test.beforeEach(async ({ request, server }) => {
  await ensureAdopted(server, request);
});

test("drag, handles and fields select the range; the limits are enforced before any estimate", async ({ page, request, server }) => {
  const watch = watchE2(page);
  const id = await base(request, server.url);
  const dur = (await apiJSON<{ duration_s: number }>(request, "GET", `/api/tracks/${id}`)).duration_s;
  await openRepaint(page, id);
  await expect(page.getByTestId("range-info")).toContainText("Drag on the waveform");
  await expect(page.getByTestId("range-info")).toContainText("at least 3 s");
  await expect(page.getByTestId("range-info")).toContainText("at most 90 s");
  await expect(page.getByTestId("action-continue")).toBeDisabled();

  // drag from 20 % to 50 % of the waveform
  const s = strip(page);
  await s.scrollIntoViewIfNeeded();
  const box = await s.boundingBox();
  if (!box) throw new Error("the waveform strip has no box");
  const y = box.y + box.height / 2;
  await page.mouse.move(box.x + box.width * 0.2, y);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width * 0.35, y, { steps: 5 });
  await page.mouse.move(box.x + box.width * 0.5, y, { steps: 5 });
  await page.mouse.up();
  expect(Math.abs((await num(page.getByTestId("range-from"))) - dur * 0.2)).toBeLessThan(dur * 0.04);
  expect(Math.abs((await num(page.getByTestId("range-to"))) - dur * 0.5)).toBeLessThan(dur * 0.04);
  await expect(page.getByTestId("range-selection")).toBeVisible();

  // keyboard: arrows move an end by 0.1 s, shift by 1 s
  const handle = page.getByTestId("range-handle-to");
  const before = Number(await handle.getAttribute("aria-valuenow"));
  await handle.focus();
  await handle.press("ArrowRight");
  expect(Number(await handle.getAttribute("aria-valuenow"))).toBeCloseTo(before + 0.1, 5);
  await handle.press("Shift+ArrowLeft");
  expect(Number(await handle.getAttribute("aria-valuenow"))).toBeCloseTo(before - 0.9, 5);

  // the fields set an exact range; a range under the engine's minimum is refused in the page
  await setRange(page, "6", "15");
  await expect(page.getByTestId("range-info")).toContainText("(9 s)");
  await expect(page.getByTestId("action-continue")).toBeEnabled();
  await setRange(page, "6", "7");
  await expect(page.getByTestId("range-problem")).toContainText("at least 3 s");
  await expect(page.getByTestId("action-continue")).toBeDisabled();
  await setRange(page, "6", "99");
  await expect(page.getByTestId("range-problem")).toBeVisible();
  await expect(page.getByTestId("action-continue")).toBeDisabled();
  expect(watch.problems()).toEqual([]);
});

test("a finger drags a range on the waveform", async ({ page, request, server, hasTouch, browserName }) => {
  test.skip(!hasTouch || browserName !== "chromium", "touch input is dispatched through the Chrome DevTools protocol: touch devices on Chromium only");
  const id = await base(request, server.url);
  await openRepaint(page, id);
  const s = strip(page);
  await s.scrollIntoViewIfNeeded();
  const box = await s.boundingBox();
  if (!box) throw new Error("the waveform strip has no box");
  const y = box.y + box.height / 2;
  const cdp = await page.context().newCDPSession(page);
  const touch = (type: "touchStart" | "touchMove" | "touchEnd", x?: number) =>
    cdp.send("Input.dispatchTouchEvent", { type, touchPoints: x === undefined ? [] : [{ x, y }] });
  await touch("touchStart", box.x + box.width * 0.2);
  for (const f of [0.3, 0.4, 0.55]) await touch("touchMove", box.x + box.width * f);
  await touch("touchEnd");
  const from = await num(page.getByTestId("range-from"));
  const to = await num(page.getByTestId("range-to"));
  expect(to - from).toBeGreaterThan(3);
  await expect(page.getByTestId("range-selection")).toBeVisible();
  // the handles are 44 px wide targets
  for (const h of ["range-handle-from", "range-handle-to"]) {
    const hb = await page.getByTestId(h).boundingBox();
    expect(hb?.width ?? 0).toBeGreaterThanOrEqual(43.5);
    expect(hb?.height ?? 0).toBeGreaterThanOrEqual(43.5);
  }
  await cdp.detach();
});

test("repaint a section: estimate shows the range, confirm, the child offers the A/B with its parent at the repaint range", async ({ page, request, server }) => {
  const watch = watchE2(page);
  const parent = await base(request, server.url);
  await openRepaint(page, parent);
  await setRange(page, "6", "15");
  await page.getByTestId("action-continue").click();

  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("intent-summary")).toContainText("repaint on ACE-Step");
  const card = page.getByTestId("estimate");
  await expect(card).toBeVisible();
  await expect(card).toContainText("engine acestep15");
  await expect(page.getByTestId("estimate-engine")).toContainText("s of GPU time each");
  await card.locator("summary", { hasText: "Takes" }).click();
  await expect(page.getByTestId("take-detail")).toContainText("6–15 s");
  await expect(page.getByTestId("hint-cross_engine_edit")).toHaveCount(0); // an ACE-Step parent is not a cross-engine edit

  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);
  const batchId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const batch = await pollBatch(request, batchId, allTerminal, 120_000);
  let child = first(batch.jobs).state === "completed" ? first(batch.jobs).track_id : null;
  if (!child) {
    // the simulated renderer collided with existing audio (DUPLICATE_AUDIO): the same repaint again, through the API
    const again = await runCompleted(request, (round) => ({ engine: ACE, items: [{ mode: "repaint", parent_track_id: parent, edit: { from_s: 6, to_s: 15 }, seeds: [500 + round] }] }));
    child = first(again.jobs).track_id;
  }
  if (!child) throw new Error("no repaint child");

  const tr = await apiJSON<{ lineage: { parent_track_id: string }; recipe: { acestep: { task: string; edit: { from_s: number; to_s: number } } } }>(request, "GET", `/api/tracks/${child}`);
  expect(tr.lineage.parent_track_id).toBe(parent);
  expect(tr.recipe.acestep).toMatchObject({ task: "repaint", edit: { from_s: 6, to_s: 15 } });

  await page.goto(`/tracks/${child}`);
  const ab = page.getByTestId("ab-repaint");
  await expect(ab).toHaveText("A/B with parent at the repaint range");
  await expect(page.getByTestId("engine-actions")).toContainText("Window 0–30 s");
  await ab.click();
  await expect(page).toHaveURL(/\/duels\/[0-9a-f-]+$/);
  const duelId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const duel = await apiJSON<{ blind: boolean; window: { from_s: number; to_s: number }; slots: { a: { track: { id: string } | null }; b: { track: { id: string } | null } } }>(request, "GET", `/api/duels/${duelId}`);
  expect(duel.blind).toBe(false);
  expect([duel.slots.a.track?.id, duel.slots.b.track?.id].sort()).toEqual([parent, child].sort());
  expect(duel.window.from_s).toBeLessThanOrEqual(6);
  expect(duel.window.to_s).toBeGreaterThanOrEqual(15);
  // the parent has no such button: it is not a repaint child
  await page.goto(`/tracks/${parent}`);
  await expect(page.getByTestId("engine-actions")).toBeVisible();
  await expect(page.getByTestId("ab-repaint")).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});

test("repainting a YuE2 take is priced with the cross-engine warning", async ({ page, request }) => {
  const watch = watchE2(page);
  const src = await trackByTitle(request, "Syn song 03");
  await openRepaint(page, src.id);
  await setRange(page, "2", "6");
  await page.getByTestId("action-continue").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("estimate")).toBeVisible();
  await expect(page.getByTestId("estimate-warnings")).toContainText("cross_engine_edit");
  await expect(page.getByTestId("hint-cross_engine_edit")).toBeVisible();
  await expect(page.getByTestId("confirm-button")).toBeVisible();
  expect(watch.problems()).toEqual([]);
});
