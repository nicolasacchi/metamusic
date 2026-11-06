// matrix: @firefox because the engine picker is plain radios and the estimate card plain DOM on Firefox too
import { expect, test } from "./fixtures";
import { trackByTitle } from "./helpers";
import { allTerminal, createHuman, estimate, first, pollBatch } from "./m3Api";
import { ACE, aceRow, adoptEngine, composeBody, ensureAdopted, rejectEngine, setIntent, watchE2 } from "./e2Api";

// One server per (project, spec file) (fixtures.ts), started with MM_ACESTEP=1 over the fake ACE-Step backend and a features
// file that proves every capability. The tests run in order: the first sees the engine before its adoption, the rest after it
// (a retry starts again from the operator's rejection, a new row of the append-only log).
test.describe.configure({ mode: "serial", timeout: 120_000 });

const SOURCE = "Syn song 03"; // a seeded YuE2 take, 12 s

test("before adoption only YuE2 can be picked, and ACE-Step says why", async ({ page, request, server }) => {
  if ((await aceRow(request)).adoption.state === "adopted") rejectEngine(server);
  const watch = watchE2(page);

  const row = await aceRow(request);
  expect(row.enabled).toBe(true);
  expect(row.selectable).toBe(false);
  expect(row.unavailable_reason).toBe("not_adopted");
  expect(row.adopt_hint).toContain("engines adopt");

  // the money gate: an estimate on the engine is refused before any row
  const src = await trackByTitle(request, SOURCE);
  const refused = await request.post("/api/generations/estimate", { data: { engine: ACE, items: [{ mode: "port", parent_track_id: src.id }] } });
  expect(refused.ok()).toBe(false);
  expect(((await refused.json()) as { code: string }).code).toBe("ENGINE_NOT_ADOPTED");

  // the picker: a YuE2 variant of a take, then "Change engine"
  await page.goto(`/tracks/${src.id}`);
  await page.getByTestId("generate-button").locator("summary").click();
  await page.getByTestId("variant-new_seed").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("estimate")).toBeVisible();
  await page.getByTestId("change-engine").click();
  await expect(page.getByTestId("engine-picker")).toBeVisible();

  await expect(page.getByTestId("engine-yue2").locator("input")).toBeEnabled();
  await expect(page.getByTestId("engine-yue2").locator("input")).toBeChecked();
  await expect(page.getByTestId("engine-acestep15").locator("input")).toBeDisabled();
  await expect(page.getByTestId("engine-reason-acestep15")).toHaveText("Not adopted yet.");
  await expect(page.getByTestId("engine-hint-acestep15")).toContainText("engines adopt");
  // the YuE2 estimate is still there and nothing was refused on the way
  await expect(page.getByTestId("estimate")).toContainText("engine yue2");
  expect(watch.problems()).toEqual([]);
});

test("after the operator adopts it ACE-Step can be picked", async ({ page, request, server }) => {
  adoptEngine(server);
  const row = await aceRow(request);
  expect(row.adoption).toMatchObject({ state: "adopted", via: "cli" });
  expect(row.selectable).toBe(true);
  expect(row.unavailable_reason).toBeNull();
  expect(row.backend).toMatchObject({ enabled: true, tripped: false });

  const watch = watchE2(page);
  const src = await trackByTitle(request, SOURCE);
  await page.goto(`/tracks/${src.id}`);
  await page.getByTestId("generate-button").locator("summary").click();
  await page.getByTestId("variant-new_seed").click();
  await page.getByTestId("change-engine").click();
  await expect(page.getByTestId("engine-picker")).toBeVisible();
  const ace = page.getByTestId("engine-acestep15").locator("input");
  await expect(ace).toBeEnabled();
  await expect(page.getByTestId("engine-reason-acestep15")).toHaveCount(0);
  await expect(page.getByTestId("engine-hint-acestep15")).toHaveCount(0);
  await expect(page.getByTestId("engine-yue2").locator("input")).toBeChecked();
  expect(watch.problems()).toEqual([]);
});

test("a port to ACE-Step: the estimate card shows the engine, task class, GPU seconds and warnings; confirm creates the take", async ({ page, request, server }) => {
  await ensureAdopted(server, request);
  const watch = watchE2(page);
  const src = await trackByTitle(request, SOURCE);
  await setIntent(page, { mode: "port", engine: ACE, parent_track_id: src.id });
  await page.goto("/human/generate");

  await expect(page.getByTestId("intent-summary")).toContainText("on ACE-Step");
  await expect(page.getByTestId("engine-acestep15").locator("input")).toBeChecked();
  const card = page.getByTestId("estimate");
  await expect(card).toBeVisible();
  await expect(card).toContainText("engine acestep15");
  await expect(card).toContainText("class turbo");
  await expect(page.getByTestId("estimate-engine")).toContainText("s of GPU time each");
  await expect(page.getByTestId("cost-ceiling")).toContainText(/\$\d/);
  await expect(page.getByTestId("estimate-warnings")).toContainText("duration_estimated");
  await card.locator("summary", { hasText: "Takes" }).click();
  await expect(page.getByTestId("take-detail")).toContainText("text2music (turbo)");
  await expect(page.getByTestId("take-detail")).toContainText("cross engine");

  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);
  const batchId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const batch = await pollBatch(request, batchId, allTerminal, 120_000);
  const job = first(batch.jobs);
  // the fake renderer may collide with existing audio (DUPLICATE_AUDIO): that is the simulation, not the product
  if (job.state === "completed") {
    const child = await (await request.get(`/api/tracks/${job.track_id}`)).json();
    expect(child.recipe.engine).toBe(ACE);
    expect(child.recipe.acestep.task).toBe("text2music");
  } else expect(job.error_code).toBe("DUPLICATE_AUDIO");
  expect(watch.problems()).toEqual([]);
});

test("compose on ACE-Step: the estimate names the task and the GPU seconds, create renders a take", async ({ request, server }) => {
  await ensureAdopted(server, request);
  const body = composeBody("e2-compose", 30, 4242);
  const est = await estimate(request, "/api/generations/estimate", body);
  const take = (est as unknown as { items: { takes: { engine: string; task: string; task_class: string; expected_audio_s: number; timeout_s: number }[] }[] }).items[0]?.takes[0];
  expect(take).toMatchObject({ engine: ACE, task: "text2music", task_class: "turbo", expected_audio_s: 30 });
  expect(take?.timeout_s).toBeGreaterThan(0);

  const batch = await pollBatch(request, (await createHuman(request, est, "e2e compose")).id, allTerminal, 120_000);
  const job = first(batch.jobs);
  if (job.state === "completed") {
    const tr = await (await request.get(`/api/tracks/${job.track_id}`)).json();
    expect(tr.recipe.engine).toBe(ACE);
    expect(tr.lineage.parent_track_id).toBeNull();
  } else expect(job.error_code).toBe("DUPLICATE_AUDIO");
  // and an over-long compose is refused (never clamped)
  const over = await request.post("/api/generations/estimate", { data: composeBody("e2-over", 481, 1) });
  expect(over.status()).toBe(422);
});
