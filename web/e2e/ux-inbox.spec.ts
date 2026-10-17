// matrix: @mobile because the New inbox is the phone landing page: Play all new, the tab badge and Needs you are phone flows
import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON, openNav, uniq } from "./helpers";
import { agentCreateHeaders, allTerminal, assertOnlyDuplicateFailures, createHuman, estimate, first, pollBatch, recipe, type Batch, type Estimate } from "./m3Api";

// Every project has its own server for this file (hermetic e2e), and the specs run in order: the batch made by the first
// test is what the second one marks seen. The marker is set to "now" before anything is made, so the seed is never new.
test.describe.configure({ mode: "serial", timeout: 240_000 });

const SEEN_KEY = "mm:inbox-seen-at";
let started = "";

test.beforeAll(() => {
  started ||= new Date(Date.now() - 1000).toISOString();
});
test.beforeEach(async ({ page }) => {
  await page.addInitScript(([k, v]) => {
    if (!localStorage.getItem(k)) localStorage.setItem(k, v);
  }, [SEEN_KEY, started] as const);
});

/** One batch of three titled takes (one human confirmation); a take that collides with existing audio (simulated renderer) is made again. */
async function makeBatch(request: APIRequestContext): Promise<{ batch: Batch; titles: string[] }> {
  for (let round = 0; round < 6; round++) {
    const titles = [uniq("inbox-a"), uniq("inbox-b"), uniq("inbox-c")];
    const e = await estimate(request, "/api/generations/estimate", {
      items: titles.map((t, i) => ({ mode: "compose_new", title: t, recipe: recipe(t, 410_000 + round * 100 + i + Math.floor(Math.random() * 90_000)) })),
    });
    const b = await pollBatch(request, (await createHuman(request, e, "e2e inbox")).id, allTerminal);
    assertOnlyDuplicateFailures(b);
    if (b.jobs.every((j) => j.state === "completed")) return { batch: b, titles };
  }
  throw new Error("the e2e batch collided with existing audio too often");
}

async function idsInOrder(request: APIRequestContext, seen: string): Promise<string[]> {
  const r = await apiJSON<{ items: { id: string; title: string }[] }>(request, "GET", `/api/tracks?recorded_from=${encodeURIComponent(seen)}&sort=recorded_asc&limit=50`);
  return r.items.map((x) => x.id);
}

const newRows = (page: Page) => page.getByTestId("inbox-new").getByTestId("inbox-row");
const rowIds = (page: Page) => newRows(page).evaluateAll((els) => els.map((e) => e.getAttribute("data-track-id") ?? ""));

let made: { batch: Batch; titles: string[]; ids: string[] } | undefined;

test("a completed batch shows up in New in creation order and Play all new plays it in that order", async ({ page, request }) => {
  const { batch, titles } = await makeBatch(request);
  const ids = await idsInOrder(request, started);
  made = { batch, titles, ids };
  expect(ids.length).toBeGreaterThanOrEqual(3);

  await page.goto("/new");
  await expect(newRows(page)).toHaveCount(ids.length);
  expect(await rowIds(page)).toEqual(ids);
  await expect(page.getByTestId("new-header")).toContainText(`${ids.length} new takes`);
  // every take is unheard: a dot each
  await expect(page.getByTestId("inbox-new").getByTestId("unheard-dot")).toHaveCount(ids.length);
  // the tab badge counts them (capped at 9+)
  await expect(page.getByTestId("new-badge")).toBeVisible();

  await page.getByTestId("play-all-new").click();
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause");
  // the queue is the new takes, oldest first, starting with the first row
  await openNav(page, "Play queue"); // client navigation: a reload would drop the queue
  const now = await page.getByTestId("queue-now").getByRole("link").getAttribute("href");
  const rest = await page.getByTestId("queue-item").getByRole("link").evaluateAll((a) => a.map((x) => x.getAttribute("href") ?? ""));
  const queue = [now, ...rest].map((h) => (h ?? "").replace("/tracks/", ""));
  expect(queue.filter((q) => ids.includes(q))).toEqual(ids);
  expect(queue[0]).toBe(ids[0]);
  for (const t of titles) expect(batch.jobs.some((j) => j.title === t)).toBe(true);
});

test("Mark all seen clears the new takes and the badge", async ({ page }) => {
  expect(made, "the previous test made the batch").toBeDefined();
  await page.goto("/new");
  await expect(newRows(page).first()).toBeVisible();
  // what still needs the operator (duels, approvals, proposals) keeps the badge; read it from the page
  const needsEl = page.getByTestId("inbox-needs");
  const needs = (await needsEl.count()) === 0 ? 0 : Number(await needsEl.getAttribute("data-count"));
  await page.getByTestId("mark-all-seen").click();
  await expect(newRows(page)).toHaveCount(0);
  await expect(page.getByTestId("new-empty")).toBeVisible();
  await expect(page.getByTestId("new-header")).toContainText("0 new takes");
  const badge = page.getByTestId("new-badge");
  if (needs === 0) await expect(badge).toHaveCount(0);
  else await expect(badge).toHaveText(needs > 9 ? "9+" : String(needs));
  // the marker survives a reload
  await page.reload();
  await expect(page.getByTestId("new-header")).toContainText("0 new takes");
  await expect(newRows(page)).toHaveCount(0);
});

let createdBatch: string | null = null;
test.afterEach(async ({ request }) => {
  if (!createdBatch) return;
  const id = createdBatch;
  createdBatch = null;
  const b = await request.get(`/api/batches/${id}`);
  if (b.ok() && ((await b.json()) as Batch).approval.state === "pending") {
    await request.fetch(`/human/api/batches/${id}/reject`, { method: "POST", data: { reason: "e2e cleanup" }, headers: { "Idempotency-Key": uniq("cleanup") } });
  }
});

test("an agent batch awaiting approval is listed under Needs you and links to the confirm page", async ({ page, playwright, baseURL, agentToken }) => {
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${agentToken}` } });
  try {
    const mother = first(made?.ids ?? []);
    const er = await agent.post(`/api/tracks/${mother}/variants/estimate`, { data: { mode: "new_seed", n_seeds: 2 } });
    expect(er.ok(), await er.text()).toBe(true);
    const e = (await er.json()) as Estimate;
    expect(e.requires_human_approval).toBe(true);
    const cr = await agent.post("/api/generations", { data: { confirmation_token: e.confirmation_token, rationale: "e2e inbox" }, headers: agentCreateHeaders() });
    expect(cr.status(), await cr.text()).toBe(201);
    const batch = (await cr.json()) as Batch;
    createdBatch = batch.id;

    await page.goto("/new");
    const needs = page.getByTestId("inbox-needs");
    await expect(needs).toBeVisible();
    const row = needs.locator(`[data-testid="approval-row"][data-batch="${batch.id}"]`);
    await expect(row).toBeVisible();
    await expect(row.getByTestId("approval-link")).toHaveAttribute("href", `/human/confirm/${batch.id}`);
    // the section comes before the takes
    const order = await page.evaluate(() => {
      const y = (id: string) => document.querySelector(`[data-testid="${id}"]`)?.getBoundingClientRect().top ?? -1;
      return [y("inbox-needs"), y("inbox-new")];
    });
    expect(order[0]).toBeLessThan(order[1] ?? 0);
    // and the badge counts it
    await expect(page.getByTestId("new-badge")).toBeVisible();
  } finally {
    await agent.dispose();
  }
});
