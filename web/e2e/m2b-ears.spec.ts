import { expect, test } from "@playwright/test";
import { apiJSON, audioTime, trackByTitle } from "./helpers";

// The three "Syn ears" tracks each carry one defect (C55): they are read-only here, so every project may
// open them at the same time.
const CASES = [
  { title: "Syn ears clip", label: "Clipping" },
  { title: "Syn ears gap", label: "Internal silence" },
  { title: "Syn ears loop", label: "Token loop suspected" },
];

for (const c of CASES) {
  test(`${c.title} shows the ${c.label} flag and the blind-spot list`, async ({ page, request }) => {
    const t = await trackByTitle(request, c.title);
    await page.goto(`/tracks/${t.id}`);
    const panel = page.getByTestId("ears-panel");
    await expect(panel.getByTestId("ears-flag").filter({ hasText: c.label })).toHaveCount(1, { timeout: 15_000 });
    const blind = panel.getByRole("heading", { name: "What this cannot hear" });
    await expect(blind).toBeVisible();
    expect(await panel.locator("ul.blind li").count()).toBeGreaterThan(0);
    await expect(panel.locator("ul.blind")).toContainText(/timbre/i);
  });
}

test("the spectrogram is a JPEG of at most 100000 bytes", async ({ page, request }) => {
  const t = await trackByTitle(request, "Syn ears loop");
  const jpg = page.waitForResponse((r) => /\/media\/[0-9a-f]{64}\.jpg\?exp=/.test(r.url()));
  await page.goto(`/tracks/${t.id}`);
  const r = await jpg;
  expect(r.status()).toBe(200);
  expect(r.headers()["content-type"]).toContain("image/jpeg");
  const body = await r.body();
  expect(body.length).toBeLessThanOrEqual(100_000);
  expect(body.subarray(0, 2).toString("hex")).toBe("ffd8");
  const img = page.getByTestId("spectrogram").locator("img");
  await expect(img).toBeVisible();
  await expect.poll(() => img.evaluate((e: HTMLImageElement) => e.naturalWidth)).toBeGreaterThan(0);
});

test("clicking the middle of the plot seeks the player to half the duration", async ({ page, request }) => {
  const t = await trackByTitle(request, "Syn ears loop");
  const spec = await apiJSON<{ plot: { x0: number; width: number }; width: number }>(request, "GET", `/api/tracks/${t.id}/spectrogram?inline=false`);
  await page.goto(`/tracks/${t.id}`);
  const img = page.getByTestId("spectrogram").locator("img");
  await expect(img).toBeVisible();
  await expect.poll(() => img.evaluate((e: HTMLImageElement) => e.naturalWidth)).toBeGreaterThan(0);
  await img.scrollIntoViewIfNeeded();
  const box = (await img.boundingBox())!; // measured after the scroll: mouse coordinates are viewport-relative
  const frac = (spec.plot.x0 + spec.plot.width / 2) / spec.width;
  await page.mouse.click(box.x + box.width * frac, box.y + box.height / 2);
  // playback continues from the jump target, so poll until the reading falls inside the window
  await expect.poll(async () => Math.abs((await audioTime(page)) - t.duration_s / 2), { timeout: 5000 }).toBeLessThanOrEqual(0.5);
});
