// matrix: chrome-only because it is a server-side data flow (analyze, recover, draft); the panel is the shared Track card
import { expect, test, type APIRequestContext } from "./fixtures";
import { apiJSON } from "./helpers";
import { allTerminal, createHuman, estimate, pollBatch } from "./m3Api";

// Recover a recipe from a reference take (M5b W2): Analyze through the ordinary estimate, then "Create draft from transcript" makes
// a draft (no spend, no job) whose lyrics are what the recogniser heard. The draft's render goes through the ordinary estimate.

test.describe.configure({ timeout: 120_000 });

interface Row {
  id: string;
  title: string;
}

async function lossless(request: APIRequestContext): Promise<Row> {
  const r = await apiJSON<{ items: Row[] }>(request, "GET", "/api/tracks?limit=50&recipe_known=false&lossy=false");
  const rows = [...r.items].sort((a, b) => (a.title < b.title ? -1 : 1));
  if (rows.length === 0) throw new Error("the e2e profile has lossless reference takes");
  return rows[0]!;
}

const totals = async (request: APIRequestContext) => (await apiJSON<{ totals: { settled_micro_usd: number; outstanding_micro_usd: number } }>(request, "GET", "/api/spend")).totals;

test("a reference take is analysed, then Create draft from transcript makes a draft without any spend", async ({ page, request }) => {
  const ref = await lossless(request);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));

  await page.goto(`/tracks/${ref.id}`);
  const panel = page.getByTestId("recover");
  await expect(panel.getByTestId("recover-needs-check")).toBeVisible();
  await expect(panel.getByTestId("recover-button")).toHaveCount(0);

  // Analyze is the ordinary estimate and confirmation
  await panel.getByTestId("analyze-button").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("estimate")).toBeVisible();
  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[^/]+$/);
  await expect.poll(async () => (await request.get(`/api/tracks/${ref.id}/tier-b`)).status(), { timeout: 60_000, intervals: [500, 1000] }).toBe(200);

  await page.goto(`/tracks/${ref.id}`);
  await expect(page.getByTestId("recover-button")).toBeVisible();
  await expect(page.getByTestId("recover-needs-check")).toHaveCount(0);
  const spendBefore = await totals(request);
  const jobsBefore = (await apiJSON<{ items: unknown[] }>(request, "GET", "/api/jobs?limit=100")).items.length;
  await page.getByTestId("recover-style").fill("italian vocals, soft, 90 BPM");
  await page.getByTestId("recover-button").click();

  // the e2e fake worker hears no sung line in a reference take (the lyric heard is scripted only in the Go acceptance tests), so the
  // server refuses plainly and the page says so; the success path (draft created, no spend) is the next test (it makes the same analysis through the API)
  await expect(page.getByTestId("recover-error")).toContainText("the recogniser heard no sung line");
  await expect(page).toHaveURL(new RegExp(`/tracks/${ref.id}$`));
  expect((await apiJSON<{ items: unknown[] }>(request, "GET", "/api/jobs?limit=100")).items.length).toBe(jobsBefore);
  expect(await totals(request)).toEqual(spendBefore);
  expect(errors).toEqual([]);
});

// The answer of recover_recipe is stubbed with a real draft (made through create_draft) so the page's success path runs: the draft
// opens. That the server builds that draft from a transcript, for nothing, is TestAcceptance_Ears_RecoverRecipe's.
test("Create draft from transcript opens the draft the server made, with the style the box held", async ({ page, request }) => {
  const ref = await lossless(request);
  const e = await estimate(request, "/api/generations/estimate", { items: [{ mode: "analyze", track_ids: [ref.id] }] });
  await pollBatch(request, (await createHuman(request, e, "e2e listening check")).id, allTerminal);
  await expect.poll(async () => (await request.get(`/api/tracks/${ref.id}/tier-b`)).status(), { timeout: 60_000, intervals: [500, 1000] }).toBe(200);
  const draft = await apiJSON<{ id: string }>(request, "POST", "/api/drafts", { request: { id: "rec-e2e-1", style: "italian vocals, soft, 90 BPM", lyrics: "[verse]\nla luna dorme piano\n[chorus]\nil mare canta ancora", cot: "melody", seed: 11 }, label: "Recovered e2e" }, true);
  let sent: { style?: string; cot?: string } = {};
  let key = "";
  await page.route(`**/api/tracks/${ref.id}/recover`, async (route) => {
    sent = route.request().postDataJSON() as typeof sent;
    key = route.request().headers()["idempotency-key"] ?? "";
    const d = await apiJSON<unknown>(request, "GET", `/api/drafts/${encodeURIComponent(draft.id)}`);
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({ draft: d, transcript: { language: "it", words: 8, lines: 2, sections: [{ kind: "verse", lines: 1 }, { kind: "chorus", lines: 1 }], confidence: 0.9 }, evidence: { tempo_bpm: 90, key: null, lufs_i: null, energy: null, duration_s: 10 }, style_source: "caller" }),
    });
  });
  await page.goto(`/tracks/${ref.id}`);
  await page.getByTestId("recover-style").fill("italian vocals, soft, 90 BPM");
  await page.getByTestId("recover-button").click();
  await expect(page).toHaveURL(new RegExp(`/backlog/${draft.id}$`));
  expect(sent.style).toBe("italian vocals, soft, 90 BPM");
  expect(key.length).toBeGreaterThan(0);
});

test("a take that is not a reference has no Recover panel", async ({ page, request }) => {
  const all = await apiJSON<{ items: Row[] }>(request, "GET", "/api/tracks?limit=1&recipe_known=true");
  await page.goto(`/tracks/${all.items[0]!.id}`);
  await expect(page.getByTestId("recipe-unknown")).toHaveCount(0);
  await expect(page.getByTestId("recover")).toHaveCount(0);
});
