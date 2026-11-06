// matrix: chrome-only because it sets its own 360 px phone viewport (touch, mobile emulation) on the one project; the pages' behaviour is covered by the other m5 specs
// M5 INT-W: 360 px. No horizontal overflow on the Track page (Song PR panel with its form open, heat chips, voice recorder),
// the patch page, Judge and Callbacks (list and card), and every control in the new panels is a 44 px target. The data is
// stressed with long unbroken words (comment bodies, a patch title and rationale) because that is what breaks a narrow layout.
import { expect, test, type Page } from "./fixtures";
import { addComment, closeTheLoop, logRewindSession, overflowX, proposePatch, replaceLine, spotAt, watchM5, type ClosedLoop } from "./m5Api";
import { smallTargetsIn } from "./t1Api";

test.use({ viewport: { width: 360, height: 740 }, isMobile: true, hasTouch: true });
test.describe.configure({ mode: "serial", timeout: 300_000 });

const LONG = "unbrokenwordunbrokenwordunbrokenwordunbrokenwordunbrokenwordunbrokenwordunbrokenwordunbrokenword";

let made: Promise<{ loop: ClosedLoop; longPatch: string }> | undefined;
const setup = (request: Parameters<typeof closeTheLoop>[0]) =>
  (made ??= (async () => {
    const loop = await closeTheLoop(request, "m5-layout");
    const long = await addComment(request, loop.base.id, "fix", `${LONG} ${LONG}`, spotAt(loop.base.duration_s, 0.5));
    const patch = await proposePatch(request, loop.base.id, [long.id], `${LONG} ${LONG}`, [replaceLine(0, 0, `${LONG} ${LONG}`)], LONG);
    // a rewind, so the strip has a chip to lay out (it needs a take long enough to rewind more than a second)
    const ms = Math.floor(loop.base.duration_s * 1000);
    if (ms > 3000) await logRewindSession(request, loop.base.id, Math.floor(ms * 0.9), Math.floor(ms * 0.3));
    return { loop, longPatch: patch.id };
  })());

async function expectFits(page: Page, what: string): Promise<void> {
  const o = await overflowX(page);
  expect(o.offenders, `${what}: elements past the right edge`).toEqual([]);
  expect(o.scroll, `${what}: the page scrolls sideways (${o.scroll} > ${o.client})`).toBeLessThanOrEqual(o.client);
}

test("the Track page fits 360 px with the Song PR form open, the heat strip and the recorder", async ({ page, request }) => {
  const watch = watchM5(page);
  const { loop } = await setup(request);
  await page.goto(`/tracks/${loop.base.id}`);
  await expect(page.getByTestId("song-pr-panel")).toBeVisible();
  await expect(page.getByTestId("heat-strip")).toHaveAttribute("data-state", "ready");
  await expect(page.getByTestId("voice-recorder")).toBeVisible();
  await expect(page.getByTestId("song-pr-patches")).toBeVisible();
  await expectFits(page, "Track");

  await page.getByTestId("song-pr-open").click();
  await page.getByTestId("song-pr-pick").last().check();
  await page.getByTestId("song-pr-rationale").fill(`${LONG} ${LONG}`);
  const verse = loop.base.sections.find((s) => s.lines.length > 0);
  if (!verse) throw new Error("no section with lines");
  await page.getByTestId("song-pr-add-section").selectOption(String(verse.idx));
  await page.getByTestId("song-pr-line").first().fill(`${LONG} ${LONG}`);
  await expectFits(page, "Track with the Song PR form open");
  expect(await smallTargetsIn(page, '[data-testid="song-pr-panel"]')).toEqual([]);
  expect(await smallTargetsIn(page, '[data-testid="voice-recorder"]')).toEqual([]);
  expect(watch.problems()).toEqual([]);
});

test("the patch page fits 360 px and its controls are 44 px targets", async ({ page, request }) => {
  const watch = watchM5(page);
  const { loop, longPatch } = await setup(request);
  for (const id of [loop.patch.id, longPatch]) {
    await page.goto(`/patches/${id}`);
    await expect(page.getByTestId("patch-view")).toHaveAttribute("data-state", "ready");
    await expect(page.getByTestId("patch-line")).not.toHaveCount(0);
    await expectFits(page, `patch page ${id}`);
    expect(await smallTargetsIn(page, '[data-testid="patch-view"]')).toEqual([]);
  }
  await expect(page.getByTestId("patch-title")).toContainText(LONG);
  expect(watch.problems()).toEqual([]);
});

test("Judge with callbacks waiting, the Callbacks list and a callback card fit 360 px", async ({ page, request }) => {
  const watch = watchM5(page);
  const { loop } = await setup(request);
  await page.goto("/judge");
  await expect(page.getByTestId("callbacks-waiting")).toBeVisible();
  await expectFits(page, "Judge");
  expect(await smallTargetsIn(page, '[data-testid="callbacks-waiting"]')).toEqual([]);

  await page.getByTestId("callbacks-waiting").click();
  await expect(page.getByTestId("callbacks-list")).toBeVisible();
  await expectFits(page, "Callbacks");
  expect(await smallTargetsIn(page, '[data-testid="callbacks-list"]')).toEqual([]);

  await page.getByTestId("callbacks-list").getByRole("link").first().click();
  await expect(page.getByTestId("callback-comment")).toBeVisible();
  await expect(page.getByTestId("callback-card").locator("button[data-verdict]").first()).toBeVisible();
  await expectFits(page, "Callback card");
  expect(await smallTargetsIn(page, '[data-testid="callback-card"]')).toEqual([]);
  expect(loop.children.length).toBeGreaterThanOrEqual(2);
  expect(watch.problems()).toEqual([]);
});
