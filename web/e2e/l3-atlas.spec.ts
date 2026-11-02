// matrix: @mobile @firefox because the Atlas is a canvas (pointer and touch hit-testing, bottom-sheet selection bar) drawn per engine
import { expect as baseExpect, test, type Locator, type Page } from "./fixtures";
import { uniq } from "./helpers";
import { assignSongs, createMuse } from "./l1Api";
import { retireRecord } from "./l2Api";
import { atlasPointOf, songsAt, type TitledAt } from "./l3Api";

// Three rendered Songs at distinct tempos under one unique tag per worker server. Their features are declared tempo and a
// predicted energy (the fake renders are not analysed), so every point is hollow: the default view hides them.
const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 240_000 });

const tag = uniq("l3a");
let made: Promise<TitledAt[]> | undefined;
const songs = (request: Parameters<typeof songsAt>[0]) => (made ??= songsAt(request, tag, [80, 110, 150]));

const PAD = { left: 48, top: 10, right: 14, bottom: 30 }; // AtlasCanvas plot margins at zoom 1

/** Screen position of a map point (u right, v up) on the canvas at its home view. */
async function at(canvas: Locator, u: number, v: number): Promise<{ x: number; y: number }> {
  await canvas.scrollIntoViewIfNeeded();
  const b = await canvas.boundingBox();
  if (!b) throw new Error("the canvas has no box");
  return { x: b.x + PAD.left + u * (b.width - PAD.left - PAD.right), y: b.y + PAD.top + (1 - v) * (b.height - PAD.top - PAD.bottom) };
}

async function filterTo(page: Page, q: string) {
  await page.getByTestId("atlas-filter-q").fill(q);
  await page.getByTestId("atlas-filter-q").press("Enter");
  await expect(page).toHaveURL(new RegExp(`q=${encodeURIComponent(q).replace(/%20/g, "(%20|\\+)")}`));
}

const showPredicted = async (page: Page) => {
  await page.getByTestId("atlas-hide-predicted").uncheck();
  await expect(page.getByTestId("atlas-hidden-note")).toHaveCount(0);
};

test("the Atlas loads the spec's Songs; predicted points are hidden until asked for", async ({ page, request }) => {
  await songs(request);
  await page.goto("/atlas");
  await expect(page.getByTestId("atlas-page")).toBeVisible();
  await expect(page.getByTestId("atlas-totals")).toContainText(/plotted/);
  await expect(page.getByTestId("atlas-canvas")).toBeVisible();
  await expect(page.getByTestId("atlas-hide-predicted")).toBeChecked();
  await filterTo(page, tag);
  // every point of these Songs is predicted on energy: the note counts them and the box shows them
  await expect(page.getByTestId("atlas-hidden-note")).toContainText("3 predicted points are hidden");
  await showPredicted(page);
  await page.getByTestId("atlas-table-toggle").click();
  await expect(page.getByTestId("atlas-table-row")).toHaveCount(3);
  await page.getByTestId("atlas-hide-predicted").check();
  await expect(page.getByTestId("atlas-table-row")).toHaveCount(0);
});

test("presets switch the axes and the address bar, and Back returns to the previous map", async ({ page, request }) => {
  await songs(request);
  await page.goto("/atlas");
  await expect(page.getByTestId("atlas-preset-tempo-energy")).toHaveAttribute("aria-pressed", "true");
  await page.getByTestId("atlas-preset-family-vocal").click();
  await expect(page).toHaveURL(/x=family/);
  await expect(page.getByTestId("atlas-preset-family-vocal")).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByTestId("atlas-totals")).toContainText(/plotted/);
  await page.getByTestId("atlas-preset-requested-got").click();
  await expect(page).toHaveURL(/x=bpm_requested/);
  // the SPA history holds one entry per change: Back walks them in order
  await page.goBack();
  await expect(page).toHaveURL(/x=family/);
  await expect(page.getByTestId("atlas-preset-family-vocal")).toHaveAttribute("aria-pressed", "true");
  await page.goBack();
  await expect(page.getByTestId("atlas-preset-tempo-energy")).toHaveAttribute("aria-pressed", "true");
  await expect(page).not.toHaveURL(/x=/);
  await expect(page.getByTestId("atlas-canvas")).toBeVisible();
});

test("tapping a point opens its card; Open goes to the Song", async ({ page, request }) => {
  const [a] = await songs(request);
  const pt = await atlasPointOf(request, a!.title);
  expect(pt, "the Song is on the map").toBeDefined();
  await page.goto("/atlas");
  await filterTo(page, a!.title);
  await showPredicted(page);
  const canvas = page.getByTestId("atlas-canvas");
  await expect(canvas).toBeVisible();
  const p = await at(canvas, pt!.x, pt!.y);
  await page.mouse.click(p.x, p.y);
  const tip = page.getByTestId("atlas-tooltip");
  await expect(tip).toContainText(a!.title);
  await tip.getByRole("button", { name: "Open" }).click();
  await expect(page).toHaveURL(new RegExp(`/songs/${a!.songId}$`));
});

test("the table lists the same Songs and its checkboxes drive the selection bar", async ({ page, request }) => {
  const all = await songs(request);
  await page.goto("/atlas");
  await filterTo(page, tag);
  await showPredicted(page);
  await page.getByTestId("atlas-table-toggle").click();
  const rows = page.getByTestId("atlas-table-row");
  await expect(rows).toHaveCount(3);
  for (const s of all) await expect(rows.filter({ hasText: s.title })).toHaveCount(1);
  await expect(page.getByTestId("atlas-select-bar")).toHaveCount(0);
  await rows.first().getByRole("checkbox").check();
  await expect(page.getByTestId("atlas-selection-count")).toHaveText("1 selected");
  await page.getByTestId("atlas-clear").click();
  await expect(page.getByTestId("atlas-select-bar")).toHaveCount(0);
});

test("rectangle-select on the canvas, then New Record from selection opens the Record's Sequence tab", async ({ page, request }) => {
  const all = await songs(request);
  await page.goto("/atlas");
  await filterTo(page, tag);
  await showPredicted(page);
  const canvas = page.getByTestId("atlas-canvas");
  await expect(canvas).toBeVisible();
  const pts = await Promise.all(all.map((s) => atlasPointOf(request, s.title)));
  const us = pts.map((p) => p!.x);
  const vs = pts.map((p) => p!.y);
  // touch layouts have a Select mode button and a finger drag draws the rectangle; with a mouse it is shift-drag
  const toggle = page.getByTestId("atlas-select-toggle");
  if ((await toggle.count()) > 0) {
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-pressed", "true");
    const lo = await at(canvas, Math.min(...us) - 0.03, Math.min(...vs) - 0.06);
    const hi = await at(canvas, Math.max(...us) + 0.03, Math.max(...vs) + 0.06);
    const cdp = await page.context().newCDPSession(page);
    const touch = (type: "touchStart" | "touchMove" | "touchEnd", x: number, y: number) => cdp.send("Input.dispatchTouchEvent", { type, touchPoints: type === "touchEnd" ? [] : [{ x, y }] });
    await touch("touchStart", lo.x, lo.y);
    for (let k = 1; k <= 6; k++) await touch("touchMove", lo.x + ((hi.x - lo.x) * k) / 6, lo.y + ((hi.y - lo.y) * k) / 6);
    await touch("touchEnd", hi.x, hi.y);
  } else {
    const lo = await at(canvas, Math.min(...us) - 0.03, Math.min(...vs) - 0.06);
    const hi = await at(canvas, Math.max(...us) + 0.03, Math.max(...vs) + 0.06);
    await page.keyboard.down("Shift");
    await page.mouse.move(lo.x, lo.y);
    await page.mouse.down();
    await page.mouse.move((lo.x + hi.x) / 2, (lo.y + hi.y) / 2, { steps: 4 });
    await page.mouse.move(hi.x, hi.y, { steps: 4 });
    await page.mouse.up();
    await page.keyboard.up("Shift");
  }
  await expect(page.getByTestId("atlas-selection-count")).toHaveText("3 selected");

  const title = uniq("l3 atlas record");
  await page.getByTestId("atlas-record-title").fill(title);
  await page.getByTestId("atlas-new-record").click();
  await expect(page).toHaveURL(/\/records\/[^/?]+\?sequence=arc$/);
  await expect(page.getByTestId("record-title")).toHaveText(title);
  await expect(page.getByTestId("sequence-panel")).toBeVisible();
  const id = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByTestId("sequence-step").first()).toBeVisible();
  await retireRecord(request, id);
});

test("deep links: a Song's neighbourhood and a Muse's Songs", async ({ page, request }) => {
  const all = await songs(request);
  const muse = await createMuse(request, uniq("l3 atlas muse"));
  await assignSongs(request, muse.id, [all[0]!.songId, all[1]!.songId]);

  await page.goto(`/atlas?near=${all[1]!.songId}`);
  await expect(page.getByTestId("atlas-near-pill")).toBeVisible();
  await expect(page.getByTestId("atlas-neighbours")).toBeVisible();
  await page.getByTestId("atlas-near-pill").click();
  await expect(page.getByTestId("atlas-near-pill")).toHaveCount(0);
  await expect(page).not.toHaveURL(/near=/);

  await page.goto(`/atlas?muse=${muse.id}`);
  await expect(page.getByTestId("atlas-muse-pill")).toBeVisible();
  await showPredicted(page);
  await page.getByTestId("atlas-table-toggle").click();
  await expect(page.getByTestId("atlas-table-row")).toHaveCount(2);
  await expect(page.getByTestId("atlas-table-row").filter({ hasText: all[2]!.title })).toHaveCount(0);
});

test("the Requested vs got tab answers, with an empty state when nothing was measured", async ({ page, request }) => {
  await songs(request);
  await page.goto("/atlas");
  await page.getByTestId("atlas-tab-adherence").click();
  await expect(page.getByTestId("atlas-adherence")).toBeVisible();
  await expect(page.getByTestId("adherence-summary")).toBeVisible();
  await page.getByTestId("atlas-tab-map").click();
  await expect(page.getByTestId("atlas-canvas")).toBeVisible();
});
