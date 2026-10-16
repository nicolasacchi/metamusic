// matrix: @mobile @realtime because the player must survive an in-app route hop on the phone shell
import { expect, test, type Page } from "./fixtures";
import { apiJSON, ownTracks, uniq } from "./helpers";

// Saving a draft is an in-app hop: it must go through the SPA router, never a document load, or the player dies.
// Each test starts playback on the project's own seed track, then proves the document survived the save: a window
// marker set before the save is still there, the <audio> element is the same one, and it is still advancing.
// The spec reads no global counts and archives the drafts it creates.

type Draft = { id: string; revision: number };
const LYRICS = "[verse]\nlatu koko satu\nmora tine pasa\n\n[chorus]\nlatu kode rine\nsavi dema tupa";
const STYLE = "synthetic pop, warm, 100 bpm";

const audio = (page: Page) =>
  page.evaluate(() => {
    const a = document.querySelector("audio");
    return { id: a?.getAttribute("data-audio-id") ?? "", paused: a?.paused ?? true, t: a?.currentTime ?? -1, marker: (window as unknown as { __mmMarker?: string }).__mmMarker ?? "" };
  });

async function startPlayback(page: Page, trackId: string) {
  await page.goto(`/tracks/${trackId}`);
  await page.getByTestId("track-play").click();
  await expect.poll(async () => (await audio(page)).t, { timeout: 5000 }).toBeGreaterThan(0.2);
  const marker = uniq("doc");
  // the seed tracks are a few seconds long: loop the element so a slow run cannot end the track mid-spec
  await page.evaluate((m) => {
    (window as unknown as { __mmMarker: string }).__mmMarker = m;
    const a = document.querySelector("audio");
    if (a) a.loop = true;
  }, marker);
  return { marker, id: (await audio(page)).id };
}

async function expectStillPlaying(page: Page, before: { marker: string; id: string }) {
  const a = await audio(page);
  expect(a.marker, "the document was reloaded").toBe(before.marker);
  expect(a.id).toBe(before.id);
  expect(a.paused).toBe(false);
  await expect.poll(async () => (await audio(page)).t, { timeout: 5000 }).toBeGreaterThan(a.t);
}

async function archive(request: Parameters<typeof apiJSON>[0], id: string) {
  const cur = await apiJSON<Draft>(request, "GET", `/api/drafts/${id}`);
  await apiJSON(request, "PATCH", `/api/drafts/${id}`, { revision: cur.revision, status: "archived" });
}

test("saving a new draft keeps the player playing (no document load)", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  const before = await startPlayback(page, t.id);
  await page.getByRole("link", { name: "Backlog" }).click();
  await page.getByRole("link", { name: "New draft" }).click();
  await expect(page).toHaveURL(/\/backlog\/new$/);
  await page.getByTestId("draft-label").fill("e2e playing new");
  await page.getByTestId("draft-id").fill(uniq("e2e-play"));
  await page.getByTestId("draft-style-input").fill(STYLE);
  await page.getByTestId("draft-lyrics").fill(LYRICS);
  await page.getByTestId("draft-cot").fill("full");
  await page.getByTestId("draft-seed").fill("4243");
  await page.getByTestId("draft-create").click();
  await expect(page).toHaveURL(/\/backlog\/[0-9a-f-]{36}$/);
  await expect(page.getByTestId("draft-editor")).toBeVisible();
  const id = new URL(page.url()).pathname.split("/").pop() ?? "";
  try {
    await expectStillPlaying(page, before);
  } finally {
    await archive(request, id);
  }
});

test("saving an existing draft, and keeping mine after a conflict, keep the player playing", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  test.setTimeout(60_000);
  const label = uniq("e2e play edit");
  const made = await apiJSON<Draft>(
    request,
    "POST",
    "/api/drafts",
    { request: { id: uniq("e2e-playedit"), style: STYLE, lyrics: LYRICS, cot: "full", seed: 78 }, label },
    true,
  );
  let mineId = "";
  try {
    const before = await startPlayback(page, t.id);
    await page.getByRole("link", { name: "Backlog" }).click();
    await page.getByRole("link", { name: label }).first().click();
    await expect(page).toHaveURL(new RegExp(`/backlog/${made.id}$`));
    const lyrics = page.getByTestId("draft-lyrics");
    await expect(lyrics).toHaveValue(LYRICS);

    await lyrics.fill(LYRICS.replace("latu koko satu", "latu koko satu nemo"));
    await page.getByTestId("draft-save").click();
    await expect(page.getByTestId("draft-save")).toBeDisabled();
    await expectStillPlaying(page, before);

    // another writer bumps the revision: the next save conflicts and "keep mine" creates a new draft and opens it
    const cur = await apiJSON<Draft>(request, "GET", `/api/drafts/${made.id}`);
    await apiJSON(request, "PATCH", `/api/drafts/${made.id}`, { revision: cur.revision, label: "e2e play edit elsewhere" });
    await page.getByTestId("draft-id").fill(uniq("e2e-playmine"));
    await lyrics.fill(LYRICS.replace("latu koko satu", "latu koko satu mine"));
    await page.getByTestId("draft-save").click();
    await page.getByTestId("conflict-keep").click();
    await expect(page).not.toHaveURL(new RegExp(`/backlog/${made.id}$`));
    await expect(page).toHaveURL(/\/backlog\/[0-9a-f-]{36}$/);
    mineId = new URL(page.url()).pathname.split("/").pop() ?? "";
    await expect(page.getByTestId("draft-editor")).toBeVisible();
    await expectStillPlaying(page, before);
  } finally {
    await archive(request, made.id);
    if (mineId) await archive(request, mineId);
  }
});
