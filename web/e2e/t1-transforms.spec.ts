// matrix: @mobile @firefox because the Transform panel is a bottom sheet on phones, and Cancel and the progress bar are watched there; the render path is plain DOM on Firefox too
import { expect, test, type Locator, type Page } from "./fixtures";
import { apiJSON, openNav } from "./helpers";
import { morphBody, slowGlacierBody, t1Tracks, widenBody, type T1Tracks } from "./fixtures/t1";
import { cancelTransform, capabilities, createDone, createTransform, listTransforms, smallTargetsIn, waitFor, watchT1, type TView } from "./t1Api";

// One server per (project, spec file) (fixtures.ts), and the tests below run one after the other on it. Every test makes its own
// transforms with a spec of its own (a distinct preset, width or factor), so none of them is "reused" from another, and finds
// them by id. The renders are the real thing: the exec child over this binary on the seeded 2 to 12 second Takes.
test.describe.configure({ mode: "serial", timeout: 180_000 });

let t: T1Tracks;
test.beforeEach(async ({ request }) => {
  t = await t1Tracks(request);
});

/** The panel and the sheet (inside it). The rest of the Take page belongs to other lanes' specs (ux-touch). */
const PANEL = '[data-testid="transform-panel"]';
const card = (page: Page, id: string): Locator => page.locator(`[data-testid="transform-card"][data-id="${id}"]`);

/** Opens the form of the panel: inline on desktop, the bottom sheet on a phone. */
async function openForm(page: Page): Promise<void> {
  const first = page.locator('[data-testid="transform-open-sheet"], [data-testid="transform-kinds"]').first();
  await first.waitFor({ state: "visible" });
  if ((await page.getByTestId("transform-open-sheet").count()) > 0) {
    await page.getByTestId("transform-open-sheet").click();
    await expect(page.getByTestId("transform-sheet")).toBeVisible();
  }
  await expect(page.getByTestId("transform-kinds")).toBeVisible();
}

async function pickKind(page: Page, kind: string): Promise<void> {
  await page.getByTestId(`transform-kind-${kind}`).click();
  await expect(page.getByTestId(`transform-kind-${kind}`)).toHaveAttribute("aria-pressed", "true");
}

/** The transform the panel just created: the new one of that kind on the Take, found through the API once the click has landed. */
async function newestOn(request: Parameters<typeof listTransforms>[0], trackId: string, kind: string, known: string[]): Promise<TView> {
  let found: TView | undefined;
  await expect
    .poll(async () => {
      found = (await listTransforms(request, `&track_id=${trackId}&kind=${kind}`)).find((r) => !known.includes(r.id));
      return found?.id ?? "";
    }, { message: `a new ${kind} transform on ${trackId}`, timeout: 20_000 })
    .not.toBe("");
  if (!found) throw new Error(`no new ${kind} transform on ${trackId}`);
  return found;
}

test("master on a Take: estimate, Render, done, the new Take and A/B with the source", async ({ page, request }) => {
  const watch = watchT1(page);
  await page.goto(`/tracks/${t.source.id}`);
  await openForm(page);
  await pickKind(page, "master");

  // the free estimate appears by itself (400 ms debounce): no money, a CPU figure, the source with its resolved facts
  const line = page.getByTestId("transform-estimate-line");
  await expect(line).toContainText("$0.00");
  await expect(line).toContainText("of CPU");
  await expect(page.getByTestId("transform-estimate-input")).toContainText(t.source.title);
  await expect(page.getByTestId("transform-quota")).toBeVisible();

  const before = await listTransforms(request, `&track_id=${t.source.id}`);
  await page.getByTestId("transform-render").click();
  const row = await newestOn(request, t.source.id, "master", before.map((r) => r.id));
  const c = card(page, row.id);
  await expect(c).toBeVisible();
  await expect(c).toHaveAttribute("data-state", "done", { timeout: 120_000 });
  await expect(c.getByTestId("transform-state")).toHaveText("Done");
  await expect(c.getByTestId("transform-progress")).toHaveCount(0);

  // the new Take is a real Take next to its source, with the transform's provenance
  const done = await waitFor(request, row.id);
  expect(done.take_id).toBe(row.take_id);
  const openLink = c.getByTestId("transform-open");
  await expect(openLink).toHaveAttribute("href", `/tracks/${done.take_id}`);

  // A/B with the source: a non-blind duel of the source against the new Take
  await c.getByTestId("transform-ab").click();
  await expect(page).toHaveURL(/\/duels\/[0-9a-f-]+$/);
  const duelId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const duel = await apiJSON<{ blind: boolean; slots: { a: { track: { id: string } | null }; b: { track: { id: string } | null } } }>(request, "GET", `/api/duels/${duelId}`);
  expect(duel.blind).toBe(false);
  expect([duel.slots.a.track?.id, duel.slots.b.track?.id].sort()).toEqual([t.source.id, done.take_id].sort());

  // and the Take itself opens, marked as a transform output
  await page.goto(`/tracks/${done.take_id}`);
  await expect(page.getByTestId("origin-chip")).toHaveText("origin: transform");
  await expect(page.getByTestId("track-title")).toContainText("master");
  expect(watch.problems()).toEqual([]);
});

test("glacier shows progress and the phase while it renders, then the new Take is the source stretched", async ({ page, request }) => {
  const watch = watchT1(page);
  await page.goto(`/tracks/${t.source.id}`);
  await openForm(page);
  await pickKind(page, "glacier");
  await page.getByTestId("tf-factor").fill("10");
  await expect(page.getByTestId("transform-estimate-line")).toContainText("$0.00");
  await expect(page.getByTestId("transform-estimate-line")).toContainText("2 min of audio");

  const before = await listTransforms(request, `&track_id=${t.source.id}`);
  await page.getByTestId("transform-render").click();
  const row = await newestOn(request, t.source.id, "glacier", before.map((r) => r.id));
  const c = card(page, row.id);
  // live: a progress bar with a value and a phase word, Cancel offered, no link to a Take yet
  await expect(c.getByTestId("transform-progress")).toBeVisible();
  await expect(c.getByTestId("transform-progress")).toHaveAttribute("aria-valuenow", /^\d+$/);
  await expect(c.getByTestId("transform-phase")).toContainText("%");
  await expect(c.getByTestId("transform-cancel")).toBeVisible();
  await expect(c.getByTestId("transform-open")).toHaveCount(0);
  await expect(c).toHaveAttribute("data-state", "done", { timeout: 150_000 });
  await expect(c.getByTestId("transform-cancel")).toHaveCount(0);

  const done = await waitFor(request, row.id);
  const take = await apiJSON<{ duration_s: number; origin: string; lineage: { parent_track_id: string | null; operator: string | null } }>(request, "GET", `/api/tracks/${done.take_id}`);
  expect(take.origin).toBe("transform");
  expect(Math.abs(take.duration_s - t.source.duration_s * 10)).toBeLessThanOrEqual(1);
  expect(take.lineage.parent_track_id).toBe(t.source.id);
  expect(take.lineage.operator).toBe("glacier");
  expect(watch.problems()).toEqual([]);
});

test("Cancel stops a render, no Take appears, and /transforms shows it as cancelled", async ({ page, request }) => {
  const watch = watchT1(page);
  await page.goto(`/tracks/${t.source.id}`);
  await openForm(page);
  await pickKind(page, "glacier");
  await page.getByTestId("tf-factor").fill("12");
  await expect(page.getByTestId("transform-estimate-line")).toContainText("of CPU");

  const tracksBefore = (await apiJSON<{ items: unknown[] }>(request, "GET", "/api/tracks?limit=50")).items.length;
  const before = await listTransforms(request, `&track_id=${t.source.id}`);
  await page.getByTestId("transform-render").click();
  const row = await newestOn(request, t.source.id, "glacier", before.map((r) => r.id));
  const c = card(page, row.id);
  await expect(c.getByTestId("transform-cancel")).toBeVisible();
  await expect(c).toHaveAttribute("data-state", /^(queued|running)$/);
  await c.getByTestId("transform-cancel").click();
  await expect(c).toHaveAttribute("data-state", "cancelled", { timeout: 60_000 });
  await expect(c.getByTestId("transform-open")).toHaveCount(0);
  await expect(c.getByTestId("transform-cancel")).toHaveCount(0);

  const after = await waitFor(request, row.id);
  expect(after.state).toBe("cancelled");
  expect(after.error?.code).toBe("CANCELLED_BY_USER");
  expect((await apiJSON<{ items: unknown[] }>(request, "GET", "/api/tracks?limit=50")).items.length).toBe(tracksBefore);

  await openNav(page, "More");
  await openNav(page, "Transforms");
  await expect(page).toHaveURL(/\/transforms$/);
  await expect(card(page, row.id)).toHaveAttribute("data-state", "cancelled");
  expect(watch.problems()).toEqual([]);
});

test("morph offers the same-lyrics Take as partner and the estimate names where each tempo came from", async ({ page, request }) => {
  const watch = watchT1(page);
  // twinA and twinB are two Takes of one Song: the picker offers exactly the other one
  await page.goto(`/tracks/${t.twinA.id}`);
  await openForm(page);
  await pickKind(page, "morph");
  const select = page.getByTestId("tf-partner-select");
  await expect(select).toBeVisible();
  const options = await select.locator("option").allTextContents();
  expect(options.filter((o) => o === t.twinB.title)).toHaveLength(1);
  expect(options).toHaveLength(3); // "Choose a take", the one partner, "Another take"
  await expect(page.getByTestId("transform-render")).toBeDisabled();

  // a morph of two longer Takes: partner by id, explicit downbeats, two bars
  await page.goto(`/tracks/${t.source.id}`);
  await openForm(page);
  await pickKind(page, "morph");
  await expect(page.getByTestId("tf-no-partners")).toBeVisible();
  await page.getByTestId("tf-partner-id").fill(t.partner.id);
  await page.getByTestId("tf-downbeat_a_s").fill("0.4");
  await page.getByTestId("tf-downbeat_b_s").fill("0.2");
  await page.getByTestId("tf-bars").fill("2");
  await expect(page.getByTestId("transform-estimate-line")).toContainText("$0.00");
  await expect(page.getByTestId("transform-estimate-input")).toHaveCount(2);
  const chips = page.getByTestId("transform-source-chip");
  await expect(chips.first()).toBeVisible();
  await expect(chips.first()).toContainText(/plan|explicit|declared|measured/);
  await expect(page.getByTestId("transform-render")).toBeEnabled();

  // the caps say the same: nothing in the table is hard-coded in the panel
  const caps = await capabilities(request);
  for (const k of caps.kinds) await expect(page.getByTestId(`transform-kind-${k.kind}`)).toBeVisible();
  expect(watch.problems()).toEqual([]);
});

test("/transforms lists the queue and history, filters by state and kind, and cancels queued and running rows", async ({ page, request }) => {
  const watch = watchT1(page);
  const doneRow = await createDone(request, widenBody(t.source.id, 1.5));
  const running = await createTransform(request, slowGlacierBody(t.source.id, 13)); // takes the only slot
  const queued = await createTransform(request, widenBody(t.source.id, 1.4)); // waits behind it
  await waitFor(request, running.id, ["running"]);

  await page.goto("/transforms");
  await expect(page.getByRole("heading", { name: "Transforms" })).toBeVisible();
  await expect(page.getByTestId("transforms-quota")).toContainText("Today's CPU");
  await expect(page.getByTestId("transforms-quota")).toContainText("queue");
  const list = page.getByTestId("transforms-list");
  await expect(card(page, running.id)).toHaveAttribute("data-state", "running");
  await expect(card(page, queued.id)).toHaveAttribute("data-state", "queued");
  await expect(card(page, doneRow.id)).toHaveAttribute("data-state", "done");
  await expect(card(page, doneRow.id).getByTestId("transform-open")).toHaveAttribute("href", `/tracks/${doneRow.take_id}`);
  await expect(card(page, doneRow.id).getByTestId("transform-cancel")).toHaveCount(0);
  await expect(card(page, running.id).getByTestId("transform-progress")).toBeVisible();
  // live rows first
  const order = await list.getByTestId("transform-card").evaluateAll((els) => els.map((e) => e.getAttribute("data-id")));
  expect(order.indexOf(running.id)).toBeLessThan(order.indexOf(doneRow.id));
  expect(order.indexOf(queued.id)).toBeLessThan(order.indexOf(doneRow.id));

  // filters ask the server
  await page.getByTestId("transforms-state-filter").selectOption("queued");
  await expect(card(page, queued.id)).toBeVisible();
  await expect(card(page, running.id)).toHaveCount(0);
  await expect(card(page, doneRow.id)).toHaveCount(0);
  await page.getByTestId("transforms-state-filter").selectOption("");
  await page.getByTestId("transforms-kind-filter").selectOption("glacier");
  await expect(card(page, running.id)).toBeVisible();
  await expect(card(page, queued.id)).toHaveCount(0);
  await page.getByTestId("transforms-kind-filter").selectOption("");
  await expect(card(page, queued.id)).toBeVisible();

  // cancel the queued row, then the running one
  await card(page, queued.id).getByTestId("transform-cancel").click();
  await expect(card(page, queued.id)).toHaveAttribute("data-state", "cancelled", { timeout: 30_000 });
  await card(page, running.id).getByTestId("transform-cancel").click();
  await expect(card(page, running.id)).toHaveAttribute("data-state", "cancelled", { timeout: 60_000 });
  await expect(page.getByTestId("transform-cancel")).toHaveCount(0);
  await page.getByTestId("transforms-state-filter").selectOption("cancelled");
  await expect(card(page, queued.id)).toBeVisible();
  await expect(card(page, running.id)).toBeVisible();
  await expect(card(page, doneRow.id)).toHaveCount(0);
  await page.getByTestId("transforms-state-filter").selectOption("done");
  await expect(card(page, doneRow.id)).toBeVisible();
  await expect(card(page, running.id)).toHaveCount(0);

  expect((await waitFor(request, queued.id)).state).toBe("cancelled");
  expect((await cancelTransform(request, doneRow.id).catch(() => null))?.state ?? "done").toBe("done"); // a finished row cannot be cancelled
  expect(watch.problems()).toEqual([]);
});

test("a morph shows its second input as an also-from edge in the lineage, and the Take page names the transform", async ({ page, request }) => {
  const watch = watchT1(page);
  const morph = await createDone(request, morphBody(t.source.id, t.partner.id));
  expect(morph.inputs.map((i) => i.role)).toEqual(["a", "b"]);

  await page.goto(`/lineage/${t.source.id}`);
  const node = page.locator(`[data-testid="lineage-node"][data-id="${morph.take_id}"]`);
  await expect(node).toBeVisible();
  await expect(node.getByTestId("lineage-operator")).toContainText("morph");
  await expect(node.getByTestId("lineage-engine")).toHaveText("dsp · Morph");
  const also = node.getByTestId("lineage-also-from");
  await expect(also).toHaveCount(1);
  // the partner is not in this tree, so the edge names it by the first 8 characters of its id (titles come from the tree)
  await expect(also).toContainText(t.partner.id.slice(0, 8));
  await expect(also.getByTestId("lineage-also-role")).toHaveText("B");
  await expect(also.getByRole("link")).toHaveAttribute("href", `/tracks/${t.partner.id}`);
  // the first input is the parent: the edge of the tree, not an also-from
  await expect(node.getByTestId("lineage-ab-parent")).toBeVisible();

  await page.goto(`/tracks/${morph.take_id}`);
  await expect(page.getByTestId("origin-chip")).toHaveText("origin: transform");
  const prov = page.getByTestId("transform-provenance");
  await expect(prov).toBeVisible();
  await expect(prov.getByTestId("transform-engine-chip")).toHaveText("dsp · Morph");
  await expect(prov.getByTestId("transform-spec-hash")).toHaveText(morph.spec_hash.slice(0, 8));
  await prov.getByRole("link", { name: "All transforms" }).click();
  await expect(page).toHaveURL(/\/transforms$/);
  await expect(card(page, morph.id)).toHaveAttribute("data-state", "done");
  await expect(card(page, morph.id)).toContainText(t.partner.title);
  expect(watch.problems()).toEqual([]);
});

test("transforms are available: no 501/503 answer and no 'not available yet' state anywhere", async ({ page, request }) => {
  const watch = watchT1(page);
  const caps = await capabilities(request);
  expect(caps.enabled).toBe(true);

  await page.goto(`/tracks/${t.source.id}`);
  await openForm(page);
  for (const k of caps.kinds) await expect(page.getByTestId(`transform-kind-${k.kind}`)).toBeVisible();
  await expect(page.getByTestId("transform-unavailable")).toHaveCount(0);
  await expect(page.getByTestId("transform-caps-error")).toHaveCount(0);
  await expect(page.getByTestId("transform-estimate-refused")).toHaveCount(0);

  await page.goto("/transforms");
  await expect(page.getByTestId("transforms-quota")).toBeVisible();
  await expect(page.getByTestId("transforms-unavailable")).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});

test.describe("phone width (360 px)", () => {
  test.use({ viewport: { width: 360, height: 780 } });

  const noHorizontalOverflow = async (page: Page, where: string) => {
    const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, body: document.body.scrollWidth, view: window.innerWidth }));
    expect(w.doc, `${where}: document scrollWidth`).toBeLessThanOrEqual(w.view);
    expect(w.body, `${where}: body scrollWidth`).toBeLessThanOrEqual(w.view);
  };

  test("the panel sheet, every kind's form and /transforms fit the screen and every control is at least 44 px", async ({ page, request }) => {
    const watch = watchT1(page);
    // a live row and a finished one make the page carry the widest cards
    const done = await createDone(request, widenBody(t.source.id, 1.6));

    await page.goto(`/tracks/${t.source.id}`);
    await expect(page.getByTestId("transform-open-sheet")).toBeVisible();
    await noHorizontalOverflow(page, "track page, sheet closed");
    expect.soft(await smallTargetsIn(page, PANEL), "track page, panel: targets under 44x44").toEqual([]);

    await page.getByTestId("transform-open-sheet").click();
    await expect(page.getByTestId("transform-sheet")).toBeVisible();
    const caps = await capabilities(request);
    for (const k of caps.kinds) {
      await pickKind(page, k.kind);
      await expect(page.getByTestId("transform-fields")).toBeVisible();
      await noHorizontalOverflow(page, `sheet, ${k.kind}`);
      expect.soft(await smallTargetsIn(page, PANEL), `sheet, ${k.kind}: targets under 44x44`).toEqual([]);
    }
    const sheet = await page.getByTestId("transform-sheet").boundingBox();
    expect(sheet?.x ?? -1).toBeGreaterThanOrEqual(0);
    expect((sheet?.x ?? 0) + (sheet?.width ?? 999)).toBeLessThanOrEqual(360);
    await page.getByTestId("transform-sheet-close").click();
    await expect(page.getByTestId("transform-sheet")).toHaveCount(0);

    // the live row is made last, so it is still rendering (a 12 s Take stretched x30) when the page looks at it and Cancels it
    const live = await createTransform(request, slowGlacierBody(t.source.id, 30));
    await page.goto("/transforms");
    await expect(card(page, live.id)).toBeVisible();
    await expect(card(page, done.id)).toBeVisible();
    await noHorizontalOverflow(page, "/transforms");
    expect.soft(await smallTargetsIn(page, "main"), "/transforms: targets under 44x44").toEqual([]);
    await expect(card(page, live.id).getByTestId("transform-progress")).toBeVisible();
    await card(page, live.id).getByTestId("transform-cancel").click();
    await expect(card(page, live.id)).toHaveAttribute("data-state", "cancelled", { timeout: 60_000 });
    expect(watch.problems()).toEqual([]);
  });
});
