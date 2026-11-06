// matrix: @firefox because the hi-fi kinds render through the same panel; Firefox covers the generated form controls (selects, number inputs) once more
import { expect, test, type Page } from "./fixtures";
import { apiJSON } from "./helpers";
import { t1Tracks, type T1Tracks } from "./fixtures/t1";
import { capabilities, listTransforms, waitFor, watchT1, type TCaps } from "./t1Api";

// The hi-fi kinds (master, widen; binaural joins when its lane merges) are never named in the panel: the form is generated from
// get_transform_capabilities, so this spec reads the table first and asserts the panel against it. A kind the server does not list
// is not asserted (binaural is checked only when the capabilities offer it).
test.describe.configure({ mode: "serial", timeout: 120_000 });

const HIFI = ["master", "widen", "binaural"];

let t: T1Tracks;
let caps: TCaps;
test.beforeEach(async ({ request }) => {
  t = await t1Tracks(request);
  caps = await capabilities(request);
});

async function openForm(page: Page): Promise<void> {
  const first = page.locator('[data-testid="transform-open-sheet"], [data-testid="transform-kinds"]').first();
  await first.waitFor({ state: "visible" });
  if ((await page.getByTestId("transform-open-sheet").count()) > 0) {
    await page.getByTestId("transform-open-sheet").click();
    await expect(page.getByTestId("transform-sheet")).toBeVisible();
  }
  await expect(page.getByTestId("transform-kinds")).toBeVisible();
}

test("the panel offers the hi-fi kinds the capabilities list, with a field for every parameter", async ({ page }) => {
  const watch = watchT1(page);
  const offered = caps.kinds.filter((k) => HIFI.includes(k.kind));
  expect(offered.map((k) => k.kind)).toEqual(expect.arrayContaining(["master", "widen"]));

  await page.goto(`/tracks/${t.source.id}`);
  await openForm(page);
  for (const k of offered) {
    await page.getByTestId(`transform-kind-${k.kind}`).click();
    await expect(page.getByTestId("transform-summary")).not.toHaveText("");
    for (const p of k.params.filter((x) => x.name !== "source_id")) {
      await expect(page.getByTestId(`tf-${p.name}`), `${k.kind}.${p.name}`).toHaveCount(1);
    }
    // nothing is required beyond the Take itself: the estimate comes by itself and Render is enabled
    await expect(page.getByTestId("transform-estimate-line")).toContainText("$0.00");
    await expect(page.getByTestId("transform-render")).toBeEnabled();
    if (k.kind === "binaural") await expect(page.getByTestId("transform-headphones")).toBeVisible();
    else await expect(page.getByTestId("transform-headphones")).toHaveCount(0);
  }
  // a kind the server does not list is not offered
  for (const k of HIFI.filter((h) => !offered.some((o) => o.kind === h))) await expect(page.getByTestId(`transform-kind-${k}`)).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});

test("master with the warm preset renders a Take of the source's length, level-matched and polished", async ({ page, request }) => {
  const watch = watchT1(page);
  const kind = caps.kinds.find((k) => k.kind === "master");
  const presets = (kind as unknown as { params: { name: string; enum?: string[] }[] }).params.find((p) => p.name === "preset")?.enum ?? [];
  expect(presets).toContain("warm");

  await page.goto(`/tracks/${t.source.id}`);
  await openForm(page);
  await page.getByTestId("transform-kind-master").click();
  const select = page.getByTestId("tf-preset");
  expect(await select.locator("option:not([value=''])").evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value))).toEqual(presets);
  await select.selectOption("warm");
  await expect(page.getByTestId("transform-estimate-line")).toContainText("$0.00");
  const before = (await listTransforms(request, `&track_id=${t.source.id}&kind=master`)).map((r) => r.id);
  await page.getByTestId("transform-render").click();
  await expect(page.locator('[data-testid="transform-card"][data-kind="master"]')).toHaveAttribute("data-state", "done", { timeout: 90_000 });

  const row = (await listTransforms(request, `&track_id=${t.source.id}&kind=master`)).find((r) => !before.includes(r.id));
  if (!row) throw new Error("the master transform was not created");
  const done = await waitFor(request, row.id);
  const take = await apiJSON<{ duration_s: number; origin: string; audio: { channels: number; sample_rate: number }; transform: { kind: string } | null }>(request, "GET", `/api/tracks/${done.take_id}`);
  expect(take.origin).toBe("transform");
  expect(take.transform?.kind).toBe("master");
  expect(take.audio.channels).toBe(2);
  expect(take.audio.sample_rate).toBe(48000);
  // master never re-times: same length as the source
  expect(Math.abs(take.duration_s - t.source.duration_s)).toBeLessThan(0.05);
  expect(watch.problems()).toEqual([]);
});

test("widen renders from the panel and its Take is marked as a dsp widen of the source", async ({ page, request }) => {
  const watch = watchT1(page);
  await page.goto(`/tracks/${t.source.id}`);
  await openForm(page);
  await page.getByTestId("transform-kind-widen").click();
  await page.getByTestId("tf-width").fill("1.5");
  await expect(page.getByTestId("transform-estimate-line")).toContainText("$0.00");
  const before = (await listTransforms(request, `&track_id=${t.source.id}&kind=widen`)).map((r) => r.id);
  await page.getByTestId("transform-render").click();
  const c = page.locator('[data-testid="transform-card"][data-kind="widen"]');
  await expect(c).toHaveAttribute("data-state", "done", { timeout: 90_000 });
  const row = (await listTransforms(request, `&track_id=${t.source.id}&kind=widen`)).find((r) => !before.includes(r.id));
  if (!row) throw new Error("the widen transform was not created");

  await c.getByTestId("transform-open").click();
  await expect(page).toHaveURL(new RegExp(`/tracks/${row.take_id}$`));
  await expect(page.getByTestId("origin-chip")).toHaveText("origin: transform");
  await expect(page.getByTestId("transform-engine-chip")).toHaveText("dsp · Widen");
  const take = await apiJSON<{ duration_s: number }>(request, "GET", `/api/tracks/${row.take_id}`);
  expect(Math.abs(take.duration_s - t.source.duration_s)).toBeLessThan(0.05);
  expect(watch.problems()).toEqual([]);
});
