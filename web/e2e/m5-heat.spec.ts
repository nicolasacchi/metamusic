// matrix: chrome-only because the hotspot jump seeks the audio element and the strip is an overlay on the waveform, whose phone layout m1-track and m5-layout cover
// M5 INT-W: the attention heat strip and its hotspot chips. The strip is fetched for known-recipe lossless takes only (a
// non-2xx answer would log a console error), lists where the human went back, and a chip jumps the player to that spot.
import { expect, test } from "./fixtures";
import { audioTime, trackByTitle } from "./helpers";
import { heatOf, logRewindSession, watchM5 } from "./m5Api";

test.describe.configure({ mode: "serial", timeout: 120_000 });

// Syn song 03: 12 s, a recipe, lossless, and a timed score (verse, chorus, outro of 4 s each), so a hotspot gets a section label.
const TITLE = "Syn song 03";

test("a take nobody has listened to has an empty strip and no hotspot chips", async ({ page, request }) => {
  const watch = watchM5(page);
  const t = await trackByTitle(request, TITLE);
  expect((await heatOf(request, t.id)).sessions).toBe(0);
  await page.goto(`/tracks/${t.id}`);
  const strip = page.getByTestId("heat-strip");
  await expect(strip).toHaveAttribute("data-state", "ready");
  await expect(page.getByTestId("heat-empty")).toHaveText("No listening recorded yet.");
  await expect(page.getByTestId("heat-hotspot")).toHaveCount(0);
  await expect(page.getByTestId("heat-unavailable")).toHaveCount(0);
  await expect(page.getByTestId("heat-error")).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});

test("a rewind makes a hotspot chip with its section, and the chip jumps the player there", async ({ page, request }) => {
  const watch = watchM5(page);
  const t = await trackByTitle(request, TITLE);
  // listened to 9 s, went back to 3 s (verse), heard it again
  await logRewindSession(request, t.id, 9000, 3000);
  const heat = await heatOf(request, t.id);
  expect(heat.sessions).toBe(1);
  expect(heat.rewinds?.reduce((a, b) => a + b, 0)).toBe(1);
  const spots = heat.hotspots ?? [];
  expect(spots.length).toBeGreaterThanOrEqual(1);
  expect(spots.length).toBeLessThanOrEqual(3);
  const spot = spots[0];
  if (!spot) throw new Error("no hotspot");
  expect(spot.from_s).toBeCloseTo(3, 0);

  await page.goto(`/tracks/${t.id}`);
  const strip = page.getByTestId("heat-strip");
  await expect(strip).toHaveAttribute("data-state", "ready");
  await expect(page.getByTestId("heat-bands")).toBeVisible();
  await expect(page.getByTestId("heat-empty")).toHaveCount(0);
  const chips = page.getByTestId("heat-hotspot");
  await expect(chips).toHaveCount(spots.length);
  const chip = chips.first();
  await expect(chip).toHaveText(/\d:\d{2}–\d:\d{2}/);
  await expect(chip).toContainText(spot.label ?? "");
  expect(spot.label ?? "").toMatch(/verse/i); // the timed score names the section at the spot
  await expect(chip).toHaveAttribute("data-from", String(spot.from_s));

  // the chip jumps the loaded player to the start of the hotspot
  await page.getByTestId("track-play").click();
  await expect.poll(() => audioTime(page)).toBeGreaterThan(0.5);
  await page.getByTestId("track-play").click(); // pause
  await expect(page.getByTestId("track-play")).toHaveText("Play");
  await chip.click();
  await expect.poll(async () => Math.abs((await audioTime(page)) - spot.from_s), { timeout: 5000 }).toBeLessThanOrEqual(0.6);
  expect(watch.problems()).toEqual([]);
});

test("an agent's listening does not move the strip", async ({ page, request, playwright, baseURL, agentToken }) => {
  const watch = watchM5(page);
  const t = await trackByTitle(request, TITLE);
  const before = await heatOf(request, t.id);
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${agentToken}` } });
  try {
    // the agent "rewinds" at 6 s, three times over: it is stored (a diagnostic of its own listening) but never counted
    for (let i = 0; i < 3; i++) await logRewindSession(agent, t.id, 9000, 6000);
  } finally {
    await agent.dispose();
  }
  const after = await heatOf(request, t.id);
  expect(after.sessions).toBe(before.sessions);
  expect(after.rewinds).toEqual(before.rewinds);
  expect(after.hotspots).toEqual(before.hotspots);
  await page.goto(`/tracks/${t.id}`);
  await expect(page.getByTestId("heat-hotspot")).toHaveCount((before.hotspots ?? []).length);
  expect(watch.problems()).toEqual([]);
});

test("a reference take gets no strip request at all", async ({ page, request }) => {
  const watch = watchM5(page);
  const ref = await trackByTitle(request, "Syn ref a");
  const asked: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname.endsWith("/heat")) asked.push(r.url());
  });
  await page.goto(`/tracks/${ref.id}`);
  await expect(page.getByTestId("track-title")).toBeVisible();
  await expect(page.getByTestId("heat-strip")).toHaveCount(0);
  expect(asked).toEqual([]);
  expect(watch.problems()).toEqual([]);
});
