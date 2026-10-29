// matrix: @mobile because Crates are built from the phone Board selection and the Song page, and the Crate pages are phone surfaces
import { expect as baseExpect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON, uniq } from "./helpers";
import { archiveDrafts, letters, makeDraftSongs, type DraftSong, type SongList } from "./l1Api";
import { hideCrate, type CrateOut } from "./l2Api";

// Three draft Songs under a unique tag (no spend): the tests of the file run in order on them and on the Crates they make,
// and the spec hides its Crates and archives its drafts at the end.
const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 180_000 });

const isPhone = (page: Page) => (page.viewportSize()?.width ?? 1280) < 768;
const tag = letters("l2c");
let drafts: Promise<DraftSong[]> | undefined;
const songs = (request: APIRequestContext) => (drafts ??= makeDraftSongs(request, 3, tag, true));
const made: string[] = [];

const crateByName = async (request: APIRequestContext, name: string) => {
  const r = await apiJSON<{ items: { id: string; name: string; revision: number }[] }>(request, "GET", `/api/crates?q=${encodeURIComponent(name)}&include_hidden=true`);
  const c = r.items.find((x) => x.name === name);
  if (!c) throw new Error(`Crate ${name} missing`);
  return c;
};
const members = async (request: APIRequestContext, crateId: string) => (await apiJSON<SongList>(request, "GET", `/api/songs?crate_id=${crateId}&limit=100`)).items.map((s) => s.title).sort();

let manual = uniq("l2 manual");
const smart = uniq("l2 smart");

test("create a manual Crate from the Board selection", async ({ page, request }) => {
  const s = await songs(request);
  await page.goto(`/board?q=${tag}`);
  const chips = page.getByTestId("song-chip");
  await expect(chips).toHaveCount(3);
  if (isPhone(page)) await page.getByTestId("select-toggle").click();
  await chips.nth(0).click();
  await chips.nth(1).click();
  await expect(page.getByTestId("selection-count")).toHaveText("2 selected");
  await page.getByTestId("crate-open").click();
  await expect(page.getByTestId("board-crate-panel")).toBeVisible();
  await page.getByTestId("crate-new-name").fill(manual);
  await page.getByTestId("crate-create-add").click();
  await expect(page.getByTestId("crate-add-note")).toBeVisible();
  const c = await crateByName(request, manual);
  made.push(c.id);
  expect(await members(request, c.id)).toHaveLength(2);

  await page.goto("/crates");
  const row = page.getByTestId("crate-row").filter({ hasText: manual });
  await expect(row).toBeVisible();
  await expect(row.getByTestId("crate-row-count")).toContainText("2");
  expect(s).toHaveLength(3);
});

test("add the third Song from its Song page", async ({ page, request }) => {
  const s = await songs(request);
  const c = await crateByName(request, manual);
  const before = await members(request, c.id);
  const third = s.find((x) => !before.includes(x.title));
  if (!third) throw new Error("one draft Song is not yet in the Crate");
  await page.goto(`/songs/${third.songId}`);
  await expect(page.getByTestId("add-to-crate")).toBeVisible();
  await page.getByTestId("crate-select").selectOption({ label: manual });
  await page.getByTestId("crate-add").click();
  await expect(page.getByTestId("in-crates")).toContainText(manual);
  expect(await members(request, c.id)).toHaveLength(3);
});

test("rename the Crate on its page", async ({ page, request }) => {
  const c = await crateByName(request, manual);
  await page.goto(`/crates/${c.id}`);
  await page.getByTestId("crate-rename").click();
  await page.getByTestId("crate-name-input").fill(`${manual} renamed`);
  await page.getByTestId("crate-name-save").click();
  await expect(page.getByTestId("crate-title")).toHaveText(`${manual} renamed`);
  expect((await crateByName(request, `${manual} renamed`)).id).toBe(c.id);
  manual = `${manual} renamed`;
});

test("a smart Crate of the concept example: the live count equals the list", async ({ page, request }) => {
  await page.goto("/crates");
  await page.getByTestId("crate-new").click();
  await page.getByTestId("crate-name").fill(smart);
  await page.getByTestId("crate-kind-smart").check();
  await expect(page.getByTestId("crate-filter-builder")).toBeVisible();
  await page.getByTestId("fb-q").fill(tag);
  await page.getByTestId("fb-unheard").selectOption("true");
  await page.getByTestId("fb-pick_decided").selectOption("false");
  const live = page.getByTestId("crate-live-count");
  await expect(live).toContainText("3");
  // the count follows the filter: decided Picks only matches none of the undecided drafts
  await page.getByTestId("fb-pick_decided").selectOption("true");
  await expect(live).toContainText("0");
  await page.getByTestId("fb-pick_decided").selectOption("false");
  await expect(live).toContainText("3");
  await page.getByTestId("crate-create").click();
  await expect(page.getByTestId("crate-page")).toBeVisible();
  const c = await crateByName(request, smart);
  made.push(c.id);
  const filter = (await apiJSON<{ crate: { filter: Record<string, unknown> } }>(request, "GET", `/api/crates/${c.id}`)).crate.filter;
  expect(filter).toMatchObject({ unheard: true, pick_decided: false, q: tag });

  const pageLive = page.getByTestId("crate-live-count");
  await expect(pageLive).toContainText("3");
  await expect(page.getByTestId("member-row")).toHaveCount(3);
  expect(await members(request, c.id)).toHaveLength(3);
});

test("remove a member from the manual Crate", async ({ page, request }) => {
  const c = await crateByName(request, manual);
  await page.goto(`/crates/${c.id}`);
  await expect(page.getByTestId("member-row")).toHaveCount(3);
  await page.getByTestId("member-row").first().getByTestId("member-remove").click();
  await expect(page.getByTestId("member-row")).toHaveCount(2);
  expect(await members(request, c.id)).toHaveLength(2);
});

test("reset: Crates hidden, drafts archived", async ({ request }) => {
  for (const id of made) await hideCrate(request, id);
  const s = await songs(request);
  await archiveDrafts(request, s.map((x) => x.draftId));
  for (const id of made) expect(((await apiJSON<CrateOut>(request, "GET", `/api/crates/${id}`)).crate.hidden)).toBe(true);
});
