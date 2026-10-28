// matrix: @mobile because approvals are done on the phone; asserts the player never covers Approve/Reject
import { expect, test, type APIRequestContext, type Locator, type Page, type PlaywrightWorkerArgs } from "./fixtures";
import { ownTracks, uniq } from "./helpers";
import { agentCreateHeaders, composeRoots, first, getBatch, type Batch, type Estimate } from "./m3Api";

// Desktop Chrome, Pixel 7 and iPhone 15. Pending approvals are discoverable inside the app: nav badge, Jobs section,
// link to the confirm page. Counts are compared with the server's own pending list (other specs may leave batches
// pending), never with absolute numbers (cheap to keep with a server per worker). Desktop approves (the fake renderer runs it); the phones reject, so that the
// two fake workers stay free for the m3-variant and m3-jobs specs of the same project.

test.describe.configure({ mode: "serial", timeout: 150_000 });

// Whatever happens, the spec's batch must not stay pending for the specs that count on a quiet queue.
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

interface Own {
  batch: Batch;
  agentName: string;
  dispose: () => Promise<void>;
}

async function ownPendingBatch(request: APIRequestContext, playwright: PlaywrightWorkerArgs["playwright"], baseURL: string | undefined, agentToken: string): Promise<Own> {
  const agentName = "e2e-agent"; // the setup's PAT; its auto-approve limit is one take, so two takes wait for the operator
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${agentToken}` } });
  const mother = first(await composeRoots(request, 1, "m3u"));
  const er = await agent.post(`/api/tracks/${mother}/variants/estimate`, { data: { mode: "new_seed", n_seeds: 2 } });
  expect(er.ok(), await er.text()).toBe(true);
  const e = (await er.json()) as Estimate;
  expect(e.requires_human_approval).toBe(true);
  const cr = await agent.post("/api/generations", { data: { confirmation_token: e.confirmation_token, rationale: "e2e approvals ui" }, headers: agentCreateHeaders() });
  expect(cr.status(), await cr.text()).toBe(201);
  const batch = (await cr.json()) as Batch;
  createdBatch = batch.id;
  expect(batch.state).toBe("awaiting_confirmation");
  return { batch, agentName, dispose: () => agent.dispose() };
}

// Scrolls a control to the middle of the viewport, as a user does.
async function centre(target: Locator): Promise<void> {
  await target.evaluate((el) => el.scrollIntoView({ block: "center", inline: "nearest" }));
}

// A control is usable when the fixed player bar does not overlap it once it is centred: the boxes are disjoint, and the
// control is what the browser hits at its centre.
async function expectNotUnderPlayer(page: Page, target: Locator): Promise<void> {
  await centre(target);
  // the fixed bottom chrome: the player (absent on a phone while nothing is loaded) and, on a phone, the tab bar
  const footer = await page.getByTestId("player-footer").boundingBox();
  const tabs = await page.getByTestId("tabbar").boundingBox();
  const vh = page.viewportSize()?.height ?? 0;
  const tops = [footer, tabs && tabs.y > vh / 2 ? tabs : null].flatMap((x) => (x ? [x.y] : []));
  const box = await target.boundingBox();
  if (!box || tops.length === 0) throw new Error("no box for the control or the fixed bottom chrome");
  const bar = { y: Math.min(...tops) };
  expect(box.y + box.height, "control ends above the player bar").toBeLessThanOrEqual(bar.y + 0.5);
  const hit = await target.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const top = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
    return top === el || el.contains(top);
  });
  expect(hit).toBe(true);
}

// A real Playwright click (actionability checks on). On failure it names what sits on top of the control.
async function press(page: Page, target: Locator, label: string): Promise<void> {
  await target.waitFor({ state: "visible" });
  await centre(target);
  try {
    await target.click({ timeout: 20_000 });
  } catch (e) {
    const cover = await target.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const top = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
      const bar = document.querySelector('[data-testid="player-footer"]')?.getBoundingClientRect();
      const main = document.querySelector("main");
      return `${top?.tagName}.${top?.className} [${top?.getAttribute("data-testid")}] at y=${Math.round(r.y)} h=${Math.round(r.height)} vh=${window.innerHeight} barTop=${Math.round(bar?.top ?? -1)} scrollY=${Math.round(scrollY)} docH=${document.documentElement.scrollHeight} mainPadBottom=${main ? getComputedStyle(main).paddingBottom : "?"}`;
    });
    throw new Error(`click failed on ${label}; the control is covered by ${cover}`, { cause: e });
  }
}

// The page never grows wider than the viewport (a wider page shrinks the layout viewport and taps land off target; the
// takes table once did that with a long title), and the reason input does not intersect the Approve/Reject buttons.
async function expectPhoneLayoutSound(page: Page): Promise<void> {
  const vw = page.viewportSize()?.width ?? 0;
  const w = await page.evaluate(() => ({ inner: innerWidth, scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }));
  expect(w.inner, "layout viewport equals the device width").toBe(vw);
  expect(w.scroll, "no horizontal overflow").toBeLessThanOrEqual(w.client);
  const input = await page.getByTestId("decision-reason").boundingBox();
  if (!input) throw new Error("no box for the reason input");
  for (const id of ["approve", "reject"]) {
    const b = await page.getByTestId(id).boundingBox();
    if (!b) throw new Error(`no box for ${id}`);
    const apart = b.y >= input.y + input.height || input.y >= b.y + b.height || b.x >= input.x + input.width || input.x >= b.x + b.width;
    expect(apart, `the reason input and ${id} do not intersect`).toBe(true);
  }
}

async function decide(page: Page, project: string): Promise<void> {
  await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
  const approve = page.getByTestId("approve");
  const reject = page.getByTestId("reject");
  await expectPhoneLayoutSound(page);
  await expectNotUnderPlayer(page, approve);
  await expectNotUnderPlayer(page, reject);
  if (project === "m3-chrome") {
    await press(page, approve, "approve");
  } else {
    await page.getByTestId("decision-reason").fill("e2e: not now");
    await press(page, reject, "reject");
  }
  await expect(page.getByTestId("decided")).toBeVisible();
}

async function pendingCount(request: APIRequestContext): Promise<number> {
  const r = await request.get("/api/batches?state=awaiting_confirmation&limit=50");
  expect(r.ok()).toBe(true);
  return ((await r.json()) as { items: { approval: { state: string } }[] }).items.filter((b) => b.approval.state === "pending").length;
}

test("a pending agent batch shows in the nav badge, the hub and Jobs, links to the confirm page and leaves the lists when decided", async ({ page, request, playwright, baseURL, agentToken }, info) => {
  const { batch, agentName, dispose } = await ownPendingBatch(request, playwright, baseURL, agentToken);
  try {
    // the badge on the More tab of the main navigation, also on a narrow viewport: other specs may decide batches at the same time,
    // so the count is only checked against a fresh server read taken in the same poll
    await page.goto("/library");
    const badge = page.getByTestId("approvals-badge");
    await expect(badge).toBeVisible();
    expect(Number(await badge.innerText())).toBeGreaterThanOrEqual(1);
    await page.screenshot({ path: `test-results/approvals-badge-${info.project.name}.png` });

    // the badge leads to the human hub, whose Approvals section lists the batch
    await press(page, page.getByTestId("tab-more"), "More tab");
    await press(page, page.getByTestId("approvals-nav"), "Approvals row");
    await expect(page).toHaveURL(/\/human\/$/);
    const row = page.locator(`[data-testid="approval-row"][data-batch="${batch.id}"]`);
    await expect(row).toBeVisible();
    await expect(row.getByTestId("approval-by")).toHaveText(agentName);
    await expect(row.getByTestId("approval-takes")).toHaveText("2 takes");
    await expect(row.getByTestId("approval-ceiling")).toContainText(/\$\d/);
    await expect(row.getByTestId("approval-reason").first()).toBeVisible();

    // the Jobs page carries the same section; with music playing (client navigation keeps the player) the link is
    // still clear of the player bar
    const [t] = await ownTracks(request, info);
    await page.goto(`/tracks/${t.id}`);
    await press(page, page.getByTestId("track-play"), "track play");
    await expect(page.getByTestId("player-footer")).toBeVisible();
    await press(page, page.getByTestId("tab-more"), "More tab");
    await press(page, page.getByRole("main").getByRole("link", { name: "Jobs", exact: true }), "Jobs row");
    await expect(page).toHaveURL(/\/jobs$/);
    await expect(row).toBeVisible();
    await expectNotUnderPlayer(page, row.getByTestId("approval-link"));
    await page.screenshot({ path: `test-results/approvals-jobs-${info.project.name}.png` });

    // the batch page carries the same link
    await page.goto(`/batches/${batch.id}`);
    await expect(page.getByTestId("review-approve").getByRole("link")).toHaveAttribute("href", `/human/confirm/${batch.id}`);

    // the Jobs link opens the confirm page
    await page.goto("/jobs");
    await press(page, row.getByTestId("approval-link"), "approval link");
    await expect(page).toHaveURL(new RegExp(`/human/confirm/${batch.id}$`));
    await decide(page, info.project.name);
    expect((await getBatch(request, batch.id)).approval.state).not.toBe("pending");

    // decided: this batch's row is gone, and the badge agrees with a fresh read of the server (absent at zero)
    await page.goto("/jobs");
    await expect(row).toHaveCount(0);
    await expect
      .poll(async () => {
        const fresh = await pendingCount(request);
        const text = (await page.getByTestId("approvals-badge").count()) === 0 ? "0" : await page.getByTestId("approvals-badge").innerText();
        if (text === String(fresh)) return true;
        await page.reload();
        return false;
      }, { timeout: 30_000 })
      .toBe(true);
  } finally {
    await dispose();
  }
});

test("/human is the hub: both spellings land on it, it lists the batch, the link decides it and the batch moves to recent decisions", async ({ page, request, playwright, baseURL, agentToken }, info) => {
  // the bare path only redirects (the edge's two-factor rule is ^/human/); the redirect itself reveals nothing
  const bare = await request.get("/human", { maxRedirects: 0 });
  expect(bare.status()).toBe(308);
  expect(new URL(bare.headers()["location"] ?? "", "http://x").pathname).toBe("/human/");

  const { batch, dispose } = await ownPendingBatch(request, playwright, baseURL, agentToken);
  try {
    await page.goto("/human");
    await expect(page).toHaveURL(/\/human\/$/);
    await expect(page.getByRole("heading", { name: "Human" })).toBeVisible();
    for (const t of ["Generate", "Spend caps", "Tokens", "Devices"]) await expect(page.getByTestId("human-card").filter({ hasText: t })).toHaveCount(1);
    await page.goto("/human/");
    const row = page.locator(`[data-testid="approval-row"][data-batch="${batch.id}"]`);
    await expect(row).toBeVisible();
    await page.screenshot({ path: `test-results/human-hub-${info.project.name}.png` });

    await press(page, row.getByTestId("approval-link"), "approval link");
    await expect(page).toHaveURL(new RegExp(`/human/confirm/${batch.id}$`));
    await decide(page, info.project.name);

    await page.goto("/human/");
    await expect(row).toHaveCount(0);
    await expect(page.locator(`[data-testid="decision-row"][data-batch="${batch.id}"]`)).toBeVisible();
  } finally {
    await dispose();
  }
});

test("the hub retries a dropped access probe on its own and still lists the batch", async ({ page, request, playwright, baseURL, agentToken }) => {
  const { batch, dispose } = await ownPendingBatch(request, playwright, baseURL, agentToken);
  try {
    // the hub's gate probe fails once with a server error, as a dropped request or a busy database would
    let failed = 0;
    await page.route("**/human/api/devices", async (route) => {
      if (failed++ === 0) await route.fulfill({ status: 503, contentType: "application/problem+json", body: '{"code":"UNAVAILABLE","message":"e2e","details":null}' });
      else await route.continue();
    });
    await page.goto("/human/");
    await expect(page.locator(`[data-testid="approval-row"][data-batch="${batch.id}"]`)).toBeVisible();
    expect(failed).toBeGreaterThanOrEqual(2);
    await expect(page.getByTestId("secure-failed")).toHaveCount(0);
  } finally {
    await dispose();
  }
});
