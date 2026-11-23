// matrix: @firefox because the audition decodes an MP3 on the shared AudioContext (codec)
import type { APIRequestContext } from "@playwright/test";
import { expect, test, type Page } from "./fixtures";
import { allTracks, idemKey } from "./helpers";
import { capabilities } from "./t1Api";

// DESK-X0 (m5b-desk C364.3): the Composer's Desk as one journey on the fake backend, the way the Owner uses it. desk-w1 and desk-w2
// pin the pieces (notation, editor, player, forms); this file pins that they connect: a take opens in the Desk, a bar is edited, the
// edit is auditioned (when the server can synthesize: the generated test soundfont of soundfont.ts, which CI installs and demands with
// MM_REQUIRE_FLUIDSYNTH=1), the edit is rendered through the ordinary estimate and the Owner's confirmation, and new scores are planned
// through plan_seeds on the fake backend and opened from the batch. Nothing here calls a real GPU.

test.describe.configure({ timeout: 180_000 });

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

test("a take is opened in the Desk, a tempo edit is auditioned and rendered through the estimate and the Owner's confirmation", async ({ page, request }) => {
  const take = await takeWithScore(request);
  await openFromTake(page, take.id);
  const parentUrl = page.url();

  // edit: a tempo change makes a child plan that names its parent
  await page.getByTestId("edit-mode-tempo").click();
  await page.getByTestId("edit-bpm").fill("91");
  await page.getByTestId("edit-rationale").fill("journey edit");
  await page.getByTestId("edit-apply").click();
  await expect(page).not.toHaveURL(parentUrl);
  await expect(page.getByTestId("plan-parent")).toBeVisible();
  await expect(page.getByTestId("plan-rationale")).toContainText("journey edit");
  const editUrl = page.url();

  // audition: free, CPU only; without a synthesizer the page says so plainly
  const panel = page.getByTestId("audition-panel");
  if (await synthAvailable(request)) {
    const made = page.waitForResponse((r) => r.url().includes("/auditions") && r.request().method() === "POST");
    await panel.getByTestId("audition-make").click();
    expect([200, 201]).toContain((await made).status());
    await expect(panel.getByTestId("audition-item")).toHaveCount(1);
    await expect(panel.getByTestId("audition-error")).toHaveCount(0);
  } else {
    await expect(panel.getByTestId("audition-unavailable")).toContainText(/not available on this server yet/);
  }
  // an audition and an edit spent nothing: no batch exists for this plan yet
  await expect(page.getByTestId("plan-render-row")).toHaveCount(0);

  // render: the estimate first, the Owner's confirmation second; the plan then lists its render job
  await page.getByTestId("render-plan").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("intent-summary")).toContainText("render plan");
  await expect(page.getByTestId("estimate")).toBeVisible();
  await expect(page.getByTestId("cost-max")).toContainText(/\$\d/);
  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);
  await page.goto(editUrl);
  const rows = page.getByTestId("plan-render-row");
  await expect(rows).toHaveCount(1);
  await expect(rows.first().getByRole("link", { name: "batch" })).toBeVisible();
});

test("plan_seeds on the fake backend: priced, confirmed, and the plans it made open in the Desk from the batch", async ({ page, request }) => {
  const take = await takeWithScore(request);
  await page.goto("/desk");
  await expect(page.getByTestId("plan-seeds-unavailable")).toHaveCount(0); // the fake backend reports the plan capability
  await page.getByTestId("seeds-base").fill(take.id);
  await page.getByTestId("seeds-count").fill("2");
  await page.getByTestId("seeds-price").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("intent-summary")).toContainText("plan seeds");
  await expect(page.getByTestId("estimate")).toBeVisible();
  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);

  // the plan job runs on the fake worker and finishes in a few seconds; the batch then links its plans
  const link = page.getByTestId("batch-plans-link");
  await expect(link).toBeVisible({ timeout: 90_000 });
  await link.click();
  await expect(page).toHaveURL(/\/desk\?job_id=/);
  const plans = page.getByTestId("desk-plan");
  await expect(plans).toHaveCount(2);
  await plans.first().getByRole("link").click();
  await expect(page).toHaveURL(/\/desk\/plans\/[^/]+$/);
  await expect(page.getByTestId("score-view").locator("svg")).toBeVisible();
  await expect(page.getByTestId("score-notation-failed")).toHaveCount(0);
  await expect(page.getByTestId("plan-render")).toBeVisible();
});
