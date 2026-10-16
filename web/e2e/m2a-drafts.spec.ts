// matrix: chrome-only because the draft editor and uploads are data flows with no device-dependent behaviour
import { expect, test } from "./fixtures";
import { apiJSON, uniq } from "./helpers";

type Draft = { id: string; revision: number; request: { lyrics: string }; request_id: string; origin: string };

const LYRICS = "[verse]\nlatu koko satu\nmora tine pasa\n\n[chorus]\nlatu kode rine\nsavi dema tupa";
const line = (id: string, seed: number) =>
  JSON.stringify({ cot: "full", id, lyrics: LYRICS, seed, style: "synthetic pop, warm, 100 bpm" });

test("new draft on /backlog/new lands on its editor and lints clean", async ({ page, request }) => {
  const rid = uniq("e2e-new");
  await page.goto("/backlog/new");
  await page.getByTestId("draft-label").fill("e2e new");
  await page.getByTestId("draft-id").fill(rid);
  await page.getByTestId("draft-style-input").fill("synthetic pop, warm, 100 bpm");
  await page.getByTestId("draft-lyrics").fill(LYRICS);
  await page.getByTestId("draft-cot").fill("full");
  await page.getByTestId("draft-seed").fill("4242");
  await page.getByTestId("draft-create").click();
  await expect(page).toHaveURL(/\/backlog\/[0-9a-f-]{36}$/);
  await expect(page.getByTestId("draft-editor")).toBeVisible();
  await expect(page.getByTestId("draft-id")).toHaveValue(rid);
  const id = new URL(page.url()).pathname.split("/").pop() ?? "";
  const d = await apiJSON<Draft>(request, "GET", `/api/drafts/${id}`);
  expect(d.origin).toBe("editor");
  expect(d.revision).toBe(1);
  await expect(page.getByTestId("lint-summary")).toContainText("0 errors");
  // keep the backlog counts of other specs stable
  await apiJSON(request, "PATCH", `/api/drafts/${id}`, { revision: 1, status: "archived" });
});

test("varianti drafts are listed under their origin filter", async ({ page }) => {
  await page.goto("/backlog");
  await page.getByTestId("backlog-origin").selectOption("varianti_import");
  await expect(page).toHaveURL(/origin=varianti_import/);
  await expect(page.getByTestId("draft-row")).toHaveCount(1);
  await expect(page.getByTestId("draft-row").first()).toContainText("syn-song-01-v2");
  await expect(page.getByTestId("lint-errors")).toHaveCount(0);
});

test("editing a lyric line and saving bumps the revision; non-sung text is flagged", async ({ page, request }) => {
  const made = await apiJSON<Draft>(
    request,
    "POST",
    "/api/drafts",
    { request: { id: uniq("e2e-edit"), style: "synthetic pop, warm, 100 bpm", lyrics: LYRICS, cot: "full", seed: 77 }, label: "e2e edit" },
    true,
  );
  try {
    await page.goto(`/backlog/${made.id}`);
    const lyrics = page.getByTestId("draft-lyrics");
    await expect(lyrics).toHaveValue(LYRICS);
    await expect(page.getByTestId("lint-summary")).toContainText("0 errors");
    await expect(page.getByTestId("draft-save")).toBeDisabled();

    await lyrics.fill(LYRICS.replace("latu koko satu", "latu koko satu nemo"));
    await expect(page.getByTestId("draft-save")).toBeEnabled();
    await page.getByTestId("draft-save").click();
    await expect(page.getByTestId("draft-save")).toBeDisabled();
    await expect.poll(async () => (await apiJSON<Draft>(request, "GET", `/api/drafts/${made.id}`)).revision).toBe(made.revision + 1);
    const saved = await apiJSON<Draft>(request, "GET", `/api/drafts/${made.id}`);
    expect(saved.request.lyrics).toContain("latu koko satu nemo");

    // a stage direction is not sung: the lint panel says so once the debounce fires
    await lyrics.fill(LYRICS.replace("[chorus]", "[chorus]\n(repeat chorus)"));
    await expect(page.locator('[data-testid="lint-issue"][data-code="non_sung_text"]')).toBeVisible();
  } finally {
    const cur = await apiJSON<Draft>(request, "GET", `/api/drafts/${made.id}`);
    await apiJSON(request, "PATCH", `/api/drafts/${made.id}`, { revision: cur.revision, status: "archived" });
  }
});

test("upload of three synthetic lines into a new collection creates three drafts", async ({ page, request }) => {
  const collection = uniq("e2e-upload");
  const jsonl = [1, 2, 3].map((n) => line(uniq(`e2e-up${n}`), 5000 + n)).join("\n");
  await page.goto("/backlog/upload");
  await page.getByTestId("upload-text").fill(jsonl);
  await page.getByTestId("upload-collection").fill(collection);
  await page.getByTestId("upload-submit").click();
  await expect(page.getByTestId("upload-result")).toBeVisible();
  await expect(page.locator('[data-testid="upload-row"][data-state="created"]')).toHaveCount(3);
  await page.goto(`/backlog?collection=${collection}`);
  await expect(page.getByTestId("draft-row")).toHaveCount(3);
  const list = await apiJSON<{ items: { id: string; revision: number }[] }>(request, "GET", `/api/drafts?collection=${collection}&limit=10`);
  for (const d of list.items) await apiJSON(request, "PATCH", `/api/drafts/${d.id}`, { revision: d.revision, status: "archived" });
});

test("an invalid upload line reports its line number and creates nothing", async ({ page }) => {
  const collection = uniq("e2e-bad");
  const jsonl = [line(uniq("e2e-ok1"), 6001), "{ this is not json", line(uniq("e2e-ok2"), 6002)].join("\n");
  await page.goto("/backlog/upload");
  await page.getByTestId("upload-text").fill(jsonl);
  await page.getByTestId("upload-collection").fill(collection);
  await page.getByTestId("upload-submit").click();
  await expect(page.getByRole("alert")).toContainText("nothing was stored");
  await expect(page.getByTestId("upload-result")).toHaveCount(0);
  const bad = page.locator('[data-testid="upload-row"][data-state="error"]');
  await expect(bad).toHaveCount(1);
  await expect(bad).toContainText("line 2");
  await expect(page.locator('[data-testid="upload-row"][data-state="created"]')).toHaveCount(0);
  await page.goto(`/backlog?collection=${collection}`);
  await expect(page.getByTestId("draft-row")).toHaveCount(0);
});
