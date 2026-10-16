import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON, uniq } from "./helpers";
import { allTerminal, assertOnlyDuplicateFailures, first, pollBatch } from "./m3Api";

// Desktop Chrome only. "New song" composes a root track (no mother) and shows its cost.

test.describe.configure({ timeout: 240_000 });

async function newSong(page: Page, request: APIRequestContext): Promise<boolean> {
  const id = uniq("m3c");
  const title = `New song ${id}`;
  await page.goto("/");
  await page.getByTestId("new-song").click();
  await expect(page).toHaveURL(/\/human\/generate$/);

  const form = page.getByTestId("mode-form");
  await expect(form).toBeVisible();
  await form.getByLabel("Id", { exact: true }).fill(id);
  await form.getByLabel("Style", { exact: true }).fill("english, synth pop, 100 bpm");
  await form.getByLabel("Lyrics", { exact: true }).fill("[Verse]\nla la\nla\nla\nla\n[Chorus]\nla\nla\nla\nla");
  await form.getByLabel("Chain of thought", { exact: true }).fill("full");
  await form.getByLabel("Seed", { exact: true }).fill(String(8800 + Math.floor(Math.random() * 100)));
  await form.getByTestId("title").fill(title);
  // the lint runs while typing (debounced) and reports in the panel
  await expect(page.getByTestId("lint-summary")).toBeVisible();

  await form.getByTestId("estimate-button").click();
  await expect(page.getByTestId("estimate")).toContainText("1 take");
  await expect(page.getByTestId("cost-ceiling")).toContainText(/\$\d/);
  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);
  const batchId = new URL(page.url()).pathname.split("/").pop() ?? "";

  const batch = await pollBatch(request, batchId, allTerminal);
  assertOnlyDuplicateFailures(batch);
  if (first(batch.jobs).state !== "completed") return false; // the fake renderer collided with existing audio
  const track = first(batch.jobs).track_id;
  expect(track).toBeTruthy();

  // a root: no parent, its own root, with the compose operator and a cost
  const t = await apiJSON<{ title: string; lineage: { parent_track_id: string | null; root_track_id: string; generation: number } }>(request, "GET", `/api/tracks/${track}`);
  expect(t.title).toBe(title);
  expect(t.lineage.parent_track_id).toBeNull();
  expect(t.lineage.root_track_id).toBe(track);
  expect(t.lineage.generation).toBe(0);
  await page.goto(`/tracks/${track}`);
  await expect(page.getByTestId("provenance")).toBeVisible();
  await expect(page.getByTestId("prov-operator")).toContainText("compose new");
  await expect(page.getByTestId("prov-cost")).toContainText(/\$\d/);
  return true;
}

test("new song: form with live lint, estimate, spend, a root track with its cost", async ({ page, request }) => {
  for (let pass = 0; pass < 4; pass++) if (await newSong(page, request)) return;
  throw new Error("every pass collided with existing audio in the fake renderer");
});
