// matrix: @mobile @firefox because the audition decodes an MP3 on the shared AudioContext (codec) and the plan page gains phone-width panels
import type { APIRequestContext } from "@playwright/test";
import { expect, test, type Page } from "./fixtures";
import { allTracks, idemKey, smallTargets } from "./helpers";
import { capabilities } from "./t1Api";

// DESK-W2 (m5b-desk C363.2-3): listen to a plan (create_plan_audition, get_plan_audition), price a render through the ordinary
// estimate and confirmation, keep a plan as a Take, and ask for new plans (plan_seeds) only while the worker image is open for it.
// The e2e servers get the generated test soundfont when fluidsynth is installed (soundfont.ts); CI installs it and sets
// MM_REQUIRE_FLUIDSYNTH=1, which makes the audition tests demand the real path. Without fluidsynth they assert the plain notice.

test.describe.configure({ timeout: 120_000 });

interface SynthCaps {
  soundfonts?: { name: string }[];
  tools?: { fluidsynth?: { available: boolean } };
}
async function synthAvailable(request: APIRequestContext): Promise<boolean> {
  const caps = await capabilities(request);
  const full = caps as typeof caps & SynthCaps;
  const ok = caps.kinds.some((k) => k.kind === "score_render") && (full.soundfonts?.length ?? 0) > 0 && full.tools?.fluidsynth?.available === true;
  if (!ok && process.env.MM_REQUIRE_FLUIDSYNTH === "1") throw new Error("MM_REQUIRE_FLUIDSYNTH=1 but the e2e server has no synthesizer or soundfont");
  return ok;
}

/** A seed take the Desk can open: the first one whose plan the server makes. */
async function takeWithScore(request: APIRequestContext): Promise<{ id: string; title: string }> {
  const tried: string[] = [];
  for (const t of (await allTracks(request)).filter((x) => x.title.startsWith("Syn song"))) {
    const r = await request.fetch(`/api/tracks/${t.id}/plan`, { method: "POST", headers: { "Idempotency-Key": idemKey() } });
    if (r.ok()) return t;
    tried.push(`${t.title}: ${r.status()} ${await r.text()}`);
  }
  throw new Error(`no seed take opens in the Desk:\n${tried.join("\n")}`);
}

async function openFromTake(page: Page, id: string) {
  await page.goto(`/tracks/${id}`);
  await page.getByTestId("open-in-desk-button").click();
  await expect(page).toHaveURL(/\/desk\/plans\/[^/]+$/);
  await expect(page.getByTestId("plan-title")).toBeVisible();
}

test("a plan is auditioned, kept in the list and played again from a fresh link", async ({ page, request }) => {
  const take = await takeWithScore(request);
  await openFromTake(page, take.id);
  const panel = page.getByTestId("audition-panel");
  await expect(panel).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  if (!(await synthAvailable(request))) {
    await expect(panel.getByTestId("audition-unavailable")).toContainText(/not available on this server yet/);
    await expect(panel.getByTestId("audition-make")).toHaveCount(0);
    return;
  }

  expect((await smallTargets(page)).filter((s) => !/Previous|Play|Next|Seek|Now playing/.test(s.what))).toEqual([]);
  const made = page.waitForResponse((r) => r.url().includes("/auditions") && r.request().method() === "POST");
  await panel.getByTestId("audition-make").click();
  const res = await made;
  expect([200, 201]).toContain(res.status());
  await expect(panel.getByTestId("audition-item")).toHaveCount(1);
  await expect(panel.getByTestId("audition-error")).toHaveCount(0);

  // a reload lists the stored audition; playing it asks for a fresh signed link (get_plan_audition) and plays on the shared context
  await page.reload();
  await expect(panel.getByTestId("audition-item")).toHaveCount(1);
  const link = page.waitForResponse((r) => /\/auditions\/[^/]+$/.test(new URL(r.url()).pathname) && r.request().method() === "GET");
  await panel.getByTestId("audition-play").click();
  expect((await link).status()).toBe(200);
  await expect(panel.getByTestId("audition-clock")).toBeVisible({ timeout: 15_000 });
  await expect(panel.getByTestId("audition-error")).toHaveCount(0);
  expect(await page.evaluate(() => document.querySelectorAll("audio").length)).toBeLessThanOrEqual(1);

  // the same settings again are the cached file: no second row
  await panel.getByTestId("audition-play").click(); // stop
  await panel.getByTestId("audition-make").click();
  await expect(panel.getByTestId("audition-item")).toHaveCount(1);
});

test("Render prices the plan's score through the ordinary estimate, and the Owner's confirmation makes a render job on the plan", async ({ page, request }) => {
  const take = await takeWithScore(request);
  await openFromTake(page, take.id);
  const planUrl = page.url();
  await expect(page.getByTestId("plan-render")).toContainText("nothing is spent until the Owner approves");
  await page.getByTestId("render-plan").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("intent-summary")).toContainText("render plan");
  await expect(page.getByTestId("estimate")).toBeVisible();
  await expect(page.getByTestId("cost-max")).toContainText(/\$\d/);
  // nothing exists yet: the click above made an estimate, not a job
  expect(await page.getByTestId("confirm-button").isEnabled()).toBe(true);
  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);

  await page.goto(planUrl);
  const rows = page.getByTestId("plan-render-row");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("seed");
  await expect(rows.first().getByRole("link", { name: "batch" })).toBeVisible();
});

test("Keep as Take renders the plan's score to a Take, or says plainly that the synthesizer is missing", async ({ page, request }) => {
  const take = await takeWithScore(request);
  await openFromTake(page, take.id);
  const panel = page.getByTestId("keep-panel");
  await expect(panel).toBeVisible();
  if (!(await synthAvailable(request))) {
    await expect(panel.getByTestId("keep-unavailable")).toContainText(/not available on this server yet/);
    await expect(panel.getByTestId("keep-make")).toHaveCount(0);
    return;
  }
  await panel.getByTestId("keep-make").click();
  await expect(panel.getByTestId("transform-card")).toBeVisible();
  await expect(panel.getByTestId("keep-refused")).toHaveCount(0);
  await expect(panel.getByTestId("transform-card")).toHaveAttribute("data-state", "done", { timeout: 60_000 });
});

test("plan_seeds is said to be unavailable while the worker image is closed for planning, and prices through the estimate when open", async ({ page }) => {
  // closed: the real provider status with the plan capability taken out
  await page.route("**/api/provider", async (route) => {
    const res = await route.fetch();
    const body = (await res.json()) as { worker: { caps: string[] } };
    body.worker.caps = ["v2"];
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto("/desk");
  await expect(page.getByTestId("plan-seeds-unavailable")).toContainText("the worker image is not open for it (missing: plan)");
  await expect(page.getByTestId("seeds-price")).toHaveCount(0);
  await page.unroute("**/api/provider");

  // open: the form hands a plan_seeds intent to the estimate page, which answers with a price or says why not, in words
  await page.route("**/api/provider", async (route) => {
    const res = await route.fetch();
    const body = (await res.json()) as { worker: { caps: string[] } };
    body.worker.caps = ["plan", "v2"];
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto("/desk");
  await expect(page.getByTestId("seeds-price")).toBeVisible();
  await page.getByTestId("seeds-price").click();
  await expect(page.getByRole("alert").filter({ hasText: "Choose the take" })).toBeVisible(); // no take chosen: refused before any call
  await expect(page).toHaveURL(/\/desk$/);
  await page.getByTestId("seeds-base").fill("does-not-exist");
  await page.getByTestId("seeds-count").fill("4");
  await page.getByTestId("seeds-price").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("intent-summary")).toContainText("plan seeds");
  await expect(page.getByTestId("intent-summary")).toContainText("4 new score seeds");
  await expect(page.getByTestId("estimate").or(page.getByTestId("estimate-error"))).toBeVisible();
  await expect(page.getByTestId("back-to-desk")).toBeVisible();
});

test("the New page links to the Desk", async ({ page }) => {
  await page.goto("/new");
  await page.getByTestId("new-desk").click();
  await expect(page).toHaveURL(/\/desk$/);
});
