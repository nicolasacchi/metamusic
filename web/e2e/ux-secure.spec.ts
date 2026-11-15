// matrix: @mobile because secure pages are opened from the More tab on a phone, where a full-page hop used to drop the music and a blocked popup is the norm
// Wave 4a (D1): /human/* pages are SPA routes, so opening them keeps the player. The server gates are unchanged (Go
// tests pin them); here the e2e server runs in proxy mode, so the step-up is simulated at the network: the first approve
// answers 403 STEP_UP_REQUIRED and /auth/step-up is a stand-in page for the real password form / edge two-factor.
import { expect, test, type BrowserContext, type Page, type PlaywrightWorkerArgs } from "./fixtures";
import { allTracks, openNav, spaGoto } from "./helpers";
import { agentCreateHeaders, composeRoots, first, type Batch, type Estimate } from "./m3Api";

test.describe.configure({ timeout: 150_000 });

const state = (page: Page) =>
  page.evaluate(() => {
    const a = document.querySelector("audio");
    return { same: a !== null && a === (window as unknown as { __mmAudio?: Element }).__mmAudio, paused: a?.paused ?? true, t: a?.currentTime ?? -1 };
  });

/** Starts the first long seeded take and marks its <audio> element so a later check can tell it was never replaced. */
async function startMusic(page: Page, request: Parameters<typeof allTracks>[0]): Promise<{ title: string }> {
  const t = (await allTracks(request)).find((x) => x.title === "Syn song 03");
  if (!t) throw new Error("the e2e profile needs Syn song 03");
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await state(page)).t, { timeout: 10_000 }).toBeGreaterThan(0.2);
  await page.evaluate(() => {
    const el = document.querySelector("audio");
    if (el) el.loop = true; // the seeded takes are a few seconds long; a slow phone must not reach the end mid-test
    (window as unknown as { __mmAudio?: Element | null }).__mmAudio = el;
    (window as unknown as { __mmLoads?: number }).__mmLoads = performance.getEntriesByType("navigation").length;
  });
  return { title: t.title };
}

/** The same element, still playing, and its clock moved on since `since`. */
async function expectStillPlaying(page: Page, since: number): Promise<number> {
  await expect.poll(async () => await state(page), { timeout: 10_000 }).toMatchObject({ same: true, paused: false });
  await expect.poll(async () => (await state(page)).t, { timeout: 10_000 }).toBeGreaterThan(since);
  return (await state(page)).t;
}

test("Spend, Approvals and Generate from More keep the same audio element playing", async ({ page, request }) => {
  await startMusic(page, request);
  let t = 0;
  const documents: string[] = [];
  page.on("request", (r) => {
    if (r.resourceType() === "document") documents.push(new URL(r.url()).pathname);
  });
  for (const [label, url, heading] of [
    ["Spend", /\/human\/spend$/, "Spend"],
    ["Approvals", /\/human\/$/, "Human"],
    ["Generate", /\/human\/generate$/, "Generate"],
  ] as const) {
    await openNav(page, label);
    await expect(page).toHaveURL(url);
    await expect(page.getByRole("heading", { level: 1, name: heading })).toBeVisible();
    t = await expectStillPlaying(page, t);
  }
  // in-app navigation: no document was requested, and the page was never reloaded
  expect(documents).toEqual([]);
  expect(await page.evaluate(() => (window as unknown as { __mmAudio?: unknown }).__mmAudio !== undefined)).toBe(true);
});

test("a secure page shows nothing from its own reads until the gated call has succeeded", async ({ page }) => {
  let gate = "hold";
  const waiting: (() => void)[] = [];
  await page.route("**/human/api/devices", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    if (gate === "hold") await new Promise<void>((r) => waiting.push(r));
    if (gate === "refuse") return route.fulfill({ status: 403, contentType: "application/problem+json", body: JSON.stringify({ code: "FORBIDDEN", message: "no", details: {} }) });
    return route.continue();
  });
  const budgetReads: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname === "/api/budget") budgetReads.push(r.url());
  });
  await page.goto("/more");
  await page.getByRole("main").getByRole("link", { name: /^Spend/ }).click();
  await expect(page.getByTestId("secure-checking")).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Spend" })).toHaveCount(0);
  expect(budgetReads).toEqual([]); // the page's own (ungated) reads have not even been asked for
  gate = "pass";
  waiting.splice(0).forEach((r) => r());
  await expect(page.getByRole("heading", { level: 1, name: "Spend" })).toBeVisible();

  // a refused gate leaves the content out
  gate = "refuse";
  await page.goto("/more");
  await page.getByRole("main").getByRole("link", { name: /^Tokens/ }).click();
  await expect(page.getByTestId("secure-denied")).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Tokens" })).toHaveCount(0);
});

interface Own {
  batch: Batch;
  dispose: () => Promise<void>;
}

async function pendingBatch(request: Parameters<typeof allTracks>[0], playwright: PlaywrightWorkerArgs["playwright"], baseURL: string | undefined, agentToken: string): Promise<Own> {
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${agentToken}` } });
  const mother = first(await composeRoots(request, 1, "uxs"));
  const er = await agent.post(`/api/tracks/${mother}/variants/estimate`, { data: { mode: "new_seed", n_seeds: 2 } });
  expect(er.ok(), await er.text()).toBe(true);
  const e = (await er.json()) as Estimate;
  expect(e.requires_human_approval).toBe(true);
  const cr = await agent.post("/api/generations", { data: { confirmation_token: e.confirmation_token, rationale: "e2e secure" }, headers: agentCreateHeaders() });
  expect(cr.status(), await cr.text()).toBe(201);
  return { batch: (await cr.json()) as Batch, dispose: () => agent.dispose() };
}

/** The first approve answers STEP_UP_REQUIRED; `confirm()` is what the stand-in step-up page does when the operator finishes. */
async function staleStepUp(context: BrowserContext, opts: { standIn: "second-tab" | "hop" }) {
  const seen = { approves: 0, stepUpDocs: [] as string[], stale: true };
  await context.route("**/human/api/batches/*/approve", async (route) => {
    seen.approves++;
    if (seen.stale) {
      return route.fulfill({ status: 403, contentType: "application/problem+json", body: JSON.stringify({ code: "STEP_UP_REQUIRED", message: "confirm your password to continue", details: {} }) });
    }
    return route.continue();
  });
  await context.route("**/auth/step-up**", async (route) => {
    const url = new URL(route.request().url());
    seen.stepUpDocs.push(url.pathname + url.search);
    seen.stale = false; // the operator confirmed
    if (opts.standIn === "hop") {
      // the real form redirects to next once the password is right
      return route.fulfill({ status: 303, headers: { location: url.searchParams.get("next") ?? "/human/" } });
    }
    return route.fulfill({
      contentType: "text/html",
      body: `<!doctype html><title>confirmed</title><p>Confirmed</p><script>
        const c = new BroadcastChannel("mm-stepup"); c.postMessage({ type: "done" }); setTimeout(() => window.close(), 50);
      </script>`,
    });
  });
  return seen;
}

test("with a stale step-up, Approve opens the confirmation in a new tab and the original page retries; the music never stops", async ({ page, context, request, playwright, baseURL, agentToken }) => {
  const own = await pendingBatch(request, playwright, baseURL, agentToken);
  try {
    await startMusic(page, request);
    const seen = await staleStepUp(context, { standIn: "second-tab" });
    await spaGoto(page, `/human/confirm/${own.batch.id}`);
    await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
    const before = (await state(page)).t;

    const popup = context.waitForEvent("page");
    await page.getByTestId("approve").click();
    const second = await popup;
    expect(new URL(second.url()).pathname + new URL(second.url()).search).toBe("/auth/step-up?next=/auth/step-up/done");
    await expect(page.getByTestId("decided")).toBeVisible({ timeout: 20_000 }); // retried here, not reloaded
    await expect.poll(() => second.isClosed()).toBe(true); // the stand-in closes itself
    expect(seen.approves).toBe(2);
    expect(seen.stepUpDocs).toEqual(["/auth/step-up?next=/auth/step-up/done"]);
    await expectStillPlaying(page, before);
    await expect(page.getByTestId("stepup-banner")).toHaveCount(0);
  } finally {
    await own.dispose();
  }
});

test("a document load of /human/spend still reaches the server (the edge / step-up decide there)", async ({ page }) => {
  const [resp] = await Promise.all([
    page.waitForResponse((r) => new URL(r.url()).pathname === "/human/spend" && r.request().resourceType() === "document"),
    page.goto("/human/spend"),
  ]);
  expect(resp.fromServiceWorker()).toBe(false);
  expect(resp.status()).toBe(200);
  await expect(page.getByRole("heading", { level: 1, name: "Spend" })).toBeVisible();
});

test("popup blocked: the full-page hop runs, then Resume at m:ss continues the music", async ({ page, context, request, playwright, baseURL, agentToken }) => {
  const own = await pendingBatch(request, playwright, baseURL, agentToken);
  try {
    await page.addInitScript(() => {
      window.open = () => null; // a blocked popup
    });
    await startMusic(page, request);
    const seen = await staleStepUp(context, { standIn: "hop" });
    await spaGoto(page, `/human/confirm/${own.batch.id}`);
    await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
    await page.evaluate(() => {
      const el = document.querySelector("audio");
      if (el) el.currentTime = 3;
    });
    await expect.poll(async () => (await state(page)).t, { timeout: 10_000 }).toBeGreaterThanOrEqual(3);

    await page.getByTestId("approve").click();
    // the hop: /auth/step-up?next=<this page>, then back here as a fresh document with the queue restored paused
    await expect.poll(() => seen.stepUpDocs.length).toBe(1);
    expect(decodeURIComponent(seen.stepUpDocs[0] ?? "")).toBe(`/auth/step-up?next=/human/confirm/${own.batch.id}`);
    const resume = page.getByTestId("player-resume");
    await expect(resume).toBeVisible({ timeout: 20_000 });
    await expect(resume).toContainText(/Resume .* at \d+:\d\d/);
    await resume.click();
    await expect.poll(async () => (await state(page)).paused, { timeout: 10_000 }).toBe(false);
    // and the approval can be made now that the gate is open
    await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
    await page.getByTestId("approve").click();
    await expect(page.getByTestId("decided")).toBeVisible({ timeout: 20_000 });
  } finally {
    await own.dispose();
  }
});

test("U3: an installed Android PWA (display-mode: standalone) still confirms in a second tab; the music never stops", async ({ page, context, request, playwright, baseURL, agentToken }) => {
  const own = await pendingBatch(request, playwright, baseURL, agentToken);
  try {
    await page.addInitScript(() => {
      const mm = window.matchMedia.bind(window);
      window.matchMedia = (q: string) => (q.includes("display-mode: standalone") ? ({ ...mm("all"), matches: true, media: q } as MediaQueryList) : mm(q));
    });
    await startMusic(page, request);
    const seen = await staleStepUp(context, { standIn: "second-tab" });
    await spaGoto(page, `/human/confirm/${own.batch.id}`);
    await expect(page.getByTestId("batch-state")).toHaveText("awaiting confirmation");
    const before = (await state(page)).t;
    const popup = context.waitForEvent("page");
    await page.getByTestId("approve").click();
    await popup; // a second tab, not a full-page hop
    await expect(page.getByTestId("decided")).toBeVisible({ timeout: 20_000 });
    expect(seen.stepUpDocs).toEqual(["/auth/step-up?next=/auth/step-up/done"]);
    await expectStillPlaying(page, before);
  } finally {
    await own.dispose();
  }
});
