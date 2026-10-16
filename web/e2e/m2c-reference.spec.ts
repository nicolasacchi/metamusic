// matrix: chrome-only because badges, filters and the MP3 fallback are data-driven and identical in Chromium emulations (real iOS playback is in the device checklist)
import { expect, test, type APIRequestContext, type Page, type TestInfo } from "@playwright/test";
import { SEED_LOSSY_TRACKS, SEED_REFERENCE_FLACS, SEED_TRACKS, SEED_UNKNOWN_TRACKS } from "./env";
import { apiJSON, uniq } from "./helpers";

// Runs on desktop Chrome and the Pixel 7 only (playwright.config.ts). The two projects run it concurrently on
// one library: each project owns one reference FLAC for the judging steps and undoes what it changed.

interface Row {
  id: string;
  title: string;
  recipe_known: boolean;
  lossy: boolean;
  root_track_id: string;
}
interface Detail extends Row {
  lineage: { root_track_id: string; parent_track_id: string | null; inferred: boolean | null };
}

const list = async (request: APIRequestContext, q: string): Promise<Row[]> => {
  const r = await apiJSON<{ items: Row[] }>(request, "GET", `/api/tracks?limit=50&${q}`);
  return [...r.items].sort((a, b) => (a.title < b.title ? -1 : a.title > b.title ? 1 : 0));
};

function watchConsole(page: Page): string[] {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  return errors;
}

const slot = (info: TestInfo) => (info.project.name === "chrome" ? 0 : 1);

test("library filter Recipe: unknown lists exactly the reference and lossy tracks, with chips", async ({ page, request }) => {
  const errors = watchConsole(page);
  await page.goto("/");
  await expect(page.getByTestId("track-row")).toHaveCount(SEED_TRACKS);
  await page.locator('select[name="recipe_known"]').selectOption("false");
  await expect(page).toHaveURL(/recipe_known=false/);
  const rows = page.getByTestId("track-row");
  await expect(rows).toHaveCount(SEED_UNKNOWN_TRACKS);
  await expect(rows.getByTestId("chip-recipe-unknown")).toHaveCount(SEED_UNKNOWN_TRACKS);
  await expect(rows.getByTestId("chip-lossy")).toHaveCount(SEED_LOSSY_TRACKS);
  // the server agrees with the page
  expect((await list(request, "recipe_known=false")).length).toBe(SEED_UNKNOWN_TRACKS);

  await page.locator('select[name="lossy"]').selectOption("false");
  await expect(rows).toHaveCount(SEED_REFERENCE_FLACS);
  await expect(rows.getByTestId("chip-lossy")).toHaveCount(0);
  await page.getByRole("button", { name: "Clear" }).click();
  await expect(rows).toHaveCount(SEED_TRACKS);
  expect(errors).toEqual([]);
});

test("a lossy track shows its badges, plays the MP3 master and seeks", async ({ page, request }) => {
  const errors = watchConsole(page);
  const lossy = await list(request, "lossy=true");
  expect(lossy).toHaveLength(SEED_LOSSY_TRACKS);
  const t = lossy[0];
  if (!t) throw new Error("no lossy track seeded");
  const links = await apiJSON<{ flac: unknown; lossy: boolean }>(request, "GET", `/api/tracks/${t.id}/media`);
  expect(links.flac).toBeNull();
  expect(links.lossy).toBe(true);

  await page.goto(`/tracks/${t.id}`);
  const badges = page.getByTestId("track-badges");
  await expect(badges.getByTestId("chip-lossy")).toBeVisible();
  await expect(badges.getByTestId("chip-recipe-unknown")).toBeVisible();
  await expect(page.getByTestId("recipe-unknown")).toContainText(/\d+ kbps/);
  await expect(page.getByTestId("lyrics")).toHaveCount(0);
  await expect(page.getByTestId("recipe-style")).toHaveCount(0);

  const audio = () =>
    page.evaluate(() => {
      const a = document.querySelector("audio");
      return { t: a?.currentTime ?? -1, d: a?.duration ?? NaN, src: a?.currentSrc ?? "" };
    });
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audio()).src).toMatch(/\/media\/[0-9a-f]{64}\.mp3\?exp=/);
  await expect.poll(async () => (await audio()).t, { timeout: 10_000 }).toBeGreaterThan(0.3);
  await expect(page.getByTestId("player-mp3-chip")).toBeVisible();

  await page.getByTestId("track-play").click(); // pause so the clock does not drift
  await expect(page.getByTestId("track-play")).toHaveText("Play");
  const wave = page.locator("article").getByTestId("waveform");
  await wave.scrollIntoViewIfNeeded();
  const box = await wave.boundingBox();
  if (!box) throw new Error("waveform has no box");
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  const { d } = await audio();
  expect(d).toBeGreaterThan(0);
  await expect.poll(async () => Math.abs((await audio()).t - d / 2)).toBeLessThanOrEqual(0.5);
  expect(errors).toEqual([]);
});

test("a reference FLAC shows the badge, can be rated and commented", async ({ page, request }, info) => {
  const errors = watchConsole(page);
  const refs = await list(request, "recipe_known=false&lossy=false");
  expect(refs).toHaveLength(SEED_REFERENCE_FLACS);
  const t = refs[slot(info)];
  if (!t) throw new Error("no reference FLAC for this project");

  await page.goto(`/tracks/${t.id}`);
  await expect(page.getByTestId("chip-recipe-unknown")).toBeVisible();
  await expect(page.getByTestId("chip-lossy")).toHaveCount(0);
  await expect(page.getByTestId("recipe-unknown")).toBeVisible();
  await expect(page.getByTestId("recipe-unknown")).not.toContainText("kbps");

  try {
    const stars = page.getByTestId("track-judge").getByRole("button", { name: "4 stars", exact: true });
    await stars.click();
    await expect(stars).toHaveAttribute("aria-pressed", "true");
    await expect
      .poll(async () => (await apiJSON<{ rating: { human: { stars: number } | null } }>(request, "GET", `/api/tracks/${t.id}`)).rating.human?.stars)
      .toBe(4);

    const body = uniq("ref comment");
    const composer = page.getByTestId("comments-panel").getByTestId("comment-composer");
    await composer.locator("textarea").fill(body);
    await composer.getByRole("button", { name: "Comment", exact: true }).click();
    await expect(page.getByTestId("comment").filter({ hasText: body })).toBeVisible();
  } finally {
    await apiJSON(request, "PUT", `/api/tracks/${t.id}/rating`, { stars: null });
  }
  expect(errors).toEqual([]);
});

test("a reference track cannot be a draft parent (server refuses; there is no picker to offer it)", async ({ request }) => {
  const [ref] = await list(request, "recipe_known=false&lossy=false");
  if (!ref) throw new Error("no reference FLAC seeded");
  const r = await request.fetch("/api/drafts", {
    method: "POST",
    headers: { "Idempotency-Key": uniq("idem") },
    data: { request: { id: uniq("e2e-ref-parent"), style: "synthetic pop", lyrics: "[verse]\nlatu koko satu", cot: "full", seed: 5 }, parent_track_id: ref.id },
  });
  expect(r.status()).toBe(422);
  expect(((await r.json()) as { code: string }).code).toBe("RECIPE_UNKNOWN");
});

test("the export page notes the reference tracks it leaves out", async ({ page }) => {
  await page.goto("/export");
  await page.getByRole("radio", { name: "Tracks" }).check();
  await page.getByTestId("export-submit").click();
  await expect(page.getByTestId("export-result")).toBeVisible();
  await expect(page.getByTestId("export-result").locator('li[data-code="recipe_unknown_excluded"]')).toHaveCount(1);
});

test("the lossy trio appears as siblings under one root in the lineage block", async ({ page, request }) => {
  const trio = (await list(request, "lossy=true&q=trio")).filter((x) => /trio/i.test(x.title));
  expect(trio).toHaveLength(3);
  const [root, second] = trio;
  if (!root || !second) throw new Error("trio missing");
  const details = await Promise.all(trio.map((x) => apiJSON<Detail>(request, "GET", `/api/tracks/${x.id}`)));
  expect(new Set(details.map((d) => d.lineage.root_track_id)).size).toBe(1);
  expect(details.filter((d) => d.lineage.parent_track_id === null)).toHaveLength(1);

  await page.goto(`/tracks/${second.id}`);
  const lineage = page.locator("section.card", { has: page.getByRole("heading", { name: "Lineage", exact: true }) });
  await expect(lineage).toContainText("(inferred)");
  await expect(lineage.locator(`a[href="/tracks/${root.id}"]`).first()).toBeVisible();
});
