// matrix: chrome-only because the Song PR loop is server work (renders on the fake backend, the M3 confirm flow); the phone layout of the panel and the patch page is asserted at 360 px in m5-layout
// M5 INT-W: the Song PR from the Track panel to the render. Propose as the human, the patch page, Render through the M3
// confirm flow, and the cap of three renders per patch. Every test makes its own base take (a composed root), so none depends on
// the patches, comments or renders another test created; the hermetic server of this spec file is shared by the tests in order.
import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON } from "./helpers";
import { allTerminal, pollBatch } from "./m3Api";
import { addComment, comments, composeBase, getPatch, proposePatch, renderPatch, replaceLine, spotAt, watchM5, type CommentOut } from "./m5Api";

test.describe.configure({ mode: "serial", timeout: 240_000 });

const panel = (page: Page) => page.getByTestId("song-pr-panel");

/** A composed base with one open fix comment. */
async function baseWithFix(request: APIRequestContext, label: string) {
  const base = await composeBase(request, label);
  const fix = await addComment(request, base.id, "fix", `${label} the second line drags`, spotAt(base.duration_s, 0.4));
  return { base, fix };
}

test("the panel on a Take without a fix comment says so and offers no form", async ({ page, request }) => {
  const watch = watchM5(page);
  const base = await composeBase(request, "m5-nofix");
  await addComment(request, base.id, "note", "m5 a plain note is not a fix", spotAt(base.duration_s, 0.3));
  await page.goto(`/tracks/${base.id}`);
  await expect(panel(page)).toBeVisible();
  await expect(page.getByTestId("song-pr-no-fixes")).toBeVisible();
  await expect(page.getByTestId("song-pr-open")).toHaveCount(0);
  await expect(page.getByTestId("song-pr-patches")).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});

test("propose from the Track panel as the human, read the patch page, Render through the confirm flow", async ({ page, request }) => {
  const watch = watchM5(page);
  const { base, fix } = await baseWithFix(request, "m5-propose");
  const verse = base.sections.find((s) => s.lines.length > 1);
  if (!verse) throw new Error("the composed base has no section with two lines");

  await page.goto(`/tracks/${base.id}`);
  await expect(panel(page)).toBeVisible();
  const row = page.locator(`[data-testid="song-pr-fix"][data-comment-id="${fix.id}"]`);
  await expect(row).toContainText(fix.body);
  await expect(page.getByTestId("song-pr-patches")).toHaveCount(0); // nothing proposed yet

  // the form states the bounds beside the live usage, and refuses an empty draft
  await page.getByTestId("song-pr-open").click();
  await expect(page.getByTestId("song-pr-bounds")).toContainText("8 lines");
  await expect(page.getByTestId("song-pr-bounds")).toContainText("4 sections");
  await expect(page.getByTestId("song-pr-bounds")).toContainText("3 style terms");
  await expect(page.getByTestId("song-pr-usage")).toContainText("0/8 lines");
  await expect(page.getByTestId("song-pr-submit")).toBeDisabled();

  await row.getByTestId("song-pr-pick").check();
  await page.getByTestId("song-pr-rationale").fill("Tighten the second line of the verse");
  await page.getByTestId("song-pr-title").fill("Tighter verse");
  await expect(page.getByTestId("song-pr-problem")).toContainText("Change at least one line");
  await page.getByTestId("song-pr-add-section").selectOption(String(verse.idx));
  const lines = page.locator(`[data-testid="song-pr-section"][data-idx="${verse.idx}"]`).getByTestId("song-pr-line");
  await expect(lines).toHaveCount(verse.lines.length);
  const after = "la la la la la";
  await lines.nth(1).fill(after);
  await expect(page.getByTestId("song-pr-usage")).toContainText("1/8 lines");
  await expect(page.getByTestId("song-pr-usage")).toContainText("1/4 sections");
  await expect(page.getByTestId("song-pr-submit")).toBeEnabled();
  await page.getByTestId("song-pr-submit").click();

  // the patch page
  await expect(page).toHaveURL(/\/patches\/[0-9a-f-]{36}$/);
  const patchId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const view = page.getByTestId("patch-view");
  await expect(view).toHaveAttribute("data-state", "ready");
  await expect(page.getByTestId("patch-title")).toHaveText("Tighter verse");
  await expect(page.getByTestId("patch-rationale")).toHaveText("Tighten the second line of the verse");
  await expect(page.getByTestId("patch-base")).toHaveAttribute("href", `/tracks/${base.id}`);
  await expect(page.getByTestId("patch-address")).toHaveCount(1);
  await expect(page.getByTestId("patch-address")).toContainText("open");
  await expect(page.getByTestId("patch-bounds")).toContainText("1/8 lines");
  await expect(page.getByTestId("patch-bounds")).toContainText("1/4 sections");
  await expect(page.getByTestId("patch-scoped")).toContainText(verse.label);
  const change = page.getByTestId("patch-line");
  await expect(change).toHaveCount(1);
  await expect(change).toContainText(verse.lines[1] ?? "");
  await expect(change).toContainText(after);
  await expect(page.getByTestId("patch-style")).toHaveCount(0); // lyrics only
  await expect(page.getByTestId("patch-renders")).toContainText("Not rendered yet");
  await expect(page.getByTestId("patch-tally")).toContainText("no callbacks yet");
  await expect(page.getByTestId("patch-render-button")).toBeEnabled();

  // the server holds what the page shows
  const stored = await getPatch(request, patchId);
  expect(stored.operator).toBe("lyric_edit");
  expect(stored.addresses.map((a) => a.comment_id)).toEqual([fix.id]);

  // the panel on the Take lists it
  await page.goto(`/tracks/${base.id}`);
  const item = page.locator(`[data-testid="song-pr-patch"][data-patch-id="${patchId}"]`);
  await expect(item).toContainText("Tighter verse");
  await expect(item.getByTestId("song-pr-renders")).toContainText("0 renders");

  // Render: the M3 confirm flow with a song_pr intent (priced, then confirmed by the human)
  await page.goto(`/patches/${patchId}`);
  await page.getByTestId("patch-seeds").selectOption("1");
  await page.getByTestId("patch-render-button").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("estimate")).toBeVisible();
  await expect(page.getByTestId("cost-max")).toContainText(/\$\d/);
  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[0-9a-f-]+$/);
  const batchId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const batch = await pollBatch(request, batchId, allTerminal, 120_000);

  await page.goto(`/patches/${patchId}`);
  await expect(page.getByTestId("patch-render")).toHaveCount(batch.jobs.length);
  await expect(page.getByTestId("patch-render").first()).toContainText(/seed \d+ · /);
  const done = batch.jobs.filter((j) => j.state === "completed" && j.track_id);
  if (done.length > 0) {
    // the first child moved the comment to addressed and opened a callback for it
    await expect.poll(async () => (await comments(request, base.id)).find((c) => c.id === fix.id)?.state).toBe("addressed");
    await expect(page.getByTestId("patch-tally")).toContainText(/0 of \d+ answered/);
    await expect(page.getByTestId("patch-address")).toContainText("addressed");
  }
  expect(watch.problems()).toEqual([]);
});

test("a patch is rendered at most three times: the page disables Render and the server refuses a fourth", async ({ page, request }) => {
  const watch = watchM5(page);
  const { base, fix } = await baseWithFix(request, "m5-cap");
  const patch = await proposePatch(request, base.id, [fix.id], "e2e: render cap", [replaceLine(0, 0, "la la la")]);

  await page.goto(`/patches/${patch.id}`);
  await expect(page.getByTestId("patch-render-button")).toBeEnabled();
  await expect(page.getByTestId("patch-render-limit")).toHaveCount(0);

  for (let n = 1; n <= 3; n++) {
    await renderPatch(request, patch.id, 1);
    const p = await getPatch(request, patch.id);
    expect(new Set(p.renders.map((r) => r.batch_id)).size).toBe(n);
  }

  await page.reload();
  await expect(page.getByTestId("patch-render")).not.toHaveCount(0);
  await expect(page.getByTestId("patch-render-limit")).toContainText("3 renders");
  await expect(page.getByTestId("patch-render-button")).toBeDisabled();
  await expect(page.getByTestId("patch-seeds")).toBeDisabled();

  // the Track panel counts them
  await page.goto(`/tracks/${base.id}`);
  const item = page.locator(`[data-testid="song-pr-patch"][data-patch-id="${patch.id}"]`);
  await expect(item.getByTestId("song-pr-renders")).toContainText("3 renders");

  // the server is the authority: a fourth estimate is refused with its own code
  const r = await request.fetch("/api/generations/estimate", { method: "POST", data: { items: [{ mode: "song_pr", patch_id: patch.id, n_seeds: 1 }] } });
  expect(r.status()).toBe(422);
  expect(((await r.json()) as { code: string }).code).toBe("PATCH_RENDER_LIMIT");
  expect((await getPatch(request, patch.id)).renders.length).toBeGreaterThanOrEqual(3);
  expect(watch.problems()).toEqual([]);
});

test("an agent can propose through the same operation; the panel shows the patch it did not make", async ({ page, request, playwright, baseURL, agentToken }) => {
  const watch = watchM5(page);
  const { base, fix } = await baseWithFix(request, "m5-agent");
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${agentToken}` } });
  try {
    const made = await apiJSON<{ id: string; author: { kind: string } }>(
      agent,
      "POST",
      "/api/patches",
      { base_track_id: base.id, addresses_comment_ids: [fix.id], rationale: "e2e agent: shorten it", ops: [replaceLine(0, 0, "la la")] },
      true,
    );
    expect(made.author.kind).toBe("agent");
    await page.goto(`/patches/${made.id}`);
    await expect(page.getByTestId("patch-view")).toHaveAttribute("data-state", "ready");
    await expect(page.getByTestId("patch-rationale")).toHaveText("e2e agent: shorten it");
    await expect(page.getByTestId("patch-render-button")).toBeEnabled(); // rendering is the human's confirm either way
    await page.goto(`/tracks/${base.id}`);
    await expect(page.locator(`[data-testid="song-pr-patch"][data-patch-id="${made.id}"]`)).toContainText("agent");
    // the form is the operator's: it is offered once, to the human
    await expect(page.getByTestId("song-pr-open")).toBeVisible();
  } finally {
    await agent.dispose();
  }
  const open: CommentOut | undefined = (await comments(request, base.id)).find((c) => c.id === fix.id);
  expect(open?.state).toBe("open"); // proposing moves nothing
  expect(watch.problems()).toEqual([]);
});
