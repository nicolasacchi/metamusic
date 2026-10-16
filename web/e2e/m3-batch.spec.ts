import { expect, test } from "./fixtures";
import { apiJSON, uniq } from "./helpers";
import { allTerminal, assertOnlyDuplicateFailures, first, getBatch, pollBatch, recipe } from "./m3Api";

// Desktop Chrome only. The seven drafts are the profile's imported varianti draft (mother: the sibling root) plus six
// drafts this spec creates on six distinct seed roots. The seed tracks "Syn song 02..05" and "Syn ears clip/gap" are
// mothers nobody else generates from (the other M3 specs compose their own); the drafts are made here, not in the
// global setup, so the count-based backlog specs of phase 1 never see them.
// A take that collides with existing audio in the simulated renderer (DUPLICATE_AUDIO, see m3Api.ts) leaves its draft
// in the backlog; that draft is generated again in a further round, with its own estimate and confirmation.

test.describe.configure({ timeout: 300_000 });

interface Draft {
  id: string;
  origin: string;
  status: string;
  parent_track_id: string | null;
  fulfilled_by_track_id: string | null;
  request_id: string;
}

test("generate 7 drafts with one confirmation", async ({ page, request }) => {
  const tracks = await apiJSON<{ items: { id: string; title: string }[] }>(request, "GET", "/api/tracks?limit=50");
  const byTitle = (t: string) => {
    const x = tracks.items.find((i) => i.title === t);
    if (!x) throw new Error(`seed track ${t} missing`);
    return x.id;
  };
  const mothers = ["Syn song 02", "Syn song 03", "Syn song 04", "Syn song 05", "Syn ears clip", "Syn ears gap"].map(byTitle);
  const collection = uniq("m3-batch");
  const mine: Draft[] = [];
  for (const [i, m] of mothers.entries()) {
    mine.push(await apiJSON<Draft>(request, "POST", "/api/drafts", { request: recipe(uniq("m3b"), 7300 + i), parent_track_id: m, collection }, true));
  }
  const all = await apiJSON<{ items: Draft[] }>(request, "GET", "/api/drafts?status=backlog&limit=50");
  const varianti = all.items.filter((d) => d.origin === "varianti_import" && d.parent_track_id);
  expect(varianti).toHaveLength(1);
  const drafts = [...mine, first(varianti)];
  const motherOf = new Map(drafts.map((d) => [d.id, d.parent_track_id]));

  let pending = drafts;
  for (let round = 0; pending.length > 0; round++) {
    expect(round, "rounds needed because of renderer collisions").toBeLessThan(6);
    await page.goto("/backlog");
    for (const d of pending) await page.locator(`[data-testid="draft-row"][data-draft-id="${d.id}"]`).getByTestId("draft-select").check();
    await expect(page.getByTestId("generate-selected")).toContainText(`Generate (${pending.length})`);
    await page.getByTestId("generate-selected").click();

    // one estimate for all pending takes, one confirmation, no second approval
    await expect(page).toHaveURL(/\/human\/generate$/);
    await expect(page.getByTestId("estimate")).toContainText(`${pending.length} take${pending.length === 1 ? "" : "s"}`);
    if (round === 0) expect(pending).toHaveLength(7);
    await expect(page.getByTestId("approval-notes")).toHaveCount(0);
    await page.getByTestId("confirm-button").click();
    await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);
    const batchId = new URL(page.url()).pathname.split("/").pop() ?? "";

    await expect(page.getByTestId("batch-jobs").getByTestId("job-row")).toHaveCount(pending.length);
    const batch = await pollBatch(request, batchId, allTerminal, 150_000);
    assertOnlyDuplicateFailures(batch);
    expect(batch.approval.via).toBe("human_create");
    expect(batch.jobs.map((j) => j.draft_id).sort()).toEqual(pending.map((d) => d.id).sort());
    await expect(page.getByTestId("batch-jobs").locator('[data-testid="job-row"][data-state="completed"]')).toHaveCount(batch.jobs.filter((j) => j.state === "completed").length);
    expect((await getBatch(request, batchId)).counts.completed).toBe(batch.jobs.filter((j) => j.state === "completed").length);

    const left: Draft[] = [];
    for (const d of pending) if ((await apiJSON<Draft>(request, "GET", `/api/drafts/${d.id}`)).status !== "fulfilled") left.push(d);
    pending = left;
  }

  // every child is linked to its draft's mother and every draft shows fulfilled
  for (const d of drafts) {
    const after = await apiJSON<Draft>(request, "GET", `/api/drafts/${d.id}`);
    expect(after.status).toBe("fulfilled");
    const child = await apiJSON<{ lineage: { parent_track_id: string | null } }>(request, "GET", `/api/tracks/${after.fulfilled_by_track_id}`);
    expect(child.lineage.parent_track_id, `child of draft ${d.id}`).toBe(motherOf.get(d.id));
  }

  await page.goto("/backlog?status=fulfilled");
  await expect(page.getByTestId("backlog-status")).toHaveValue("fulfilled");
  for (const d of drafts) await expect(page.locator(`[data-testid="draft-row"][data-draft-id="${d.id}"]`)).toHaveCount(1);
  await page.goto("/backlog");
  for (const d of drafts) await expect(page.locator(`[data-testid="draft-row"][data-draft-id="${d.id}"]`)).toHaveCount(0);
});
