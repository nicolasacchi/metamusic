// matrix: @mobile because the Song feature chips and the "Show on the Atlas" links are phone surfaces on the Song and Muse pages
import { expect as baseExpect, test } from "./fixtures";
import { uniq } from "./helpers";
import { assignSongs, createMuse } from "./l1Api";
import { songsAt, waitFeatures, type TitledAt } from "./l3Api";

// Two rendered Songs under one unique tag and a Muse that owns them. The fake renders are not analysed, so the real values
// are declared tempo and predicted energy ("estimated"); the measured and pending chips are checked on a rewritten Song
// answer, which is the same shape the server sends once `analyze` has run.
const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 240_000 });

const tag = uniq("l3e");
let songs: TitledAt[] = [];
let museId = "";

test("setup", async ({ request }) => {
  songs = await songsAt(request, tag, [100, 120]);
  museId = (await createMuse(request, uniq("l3 entry muse"))).id;
  await assignSongs(request, museId, songs.map((s) => s.songId));
  for (const s of songs) await waitFeatures(request, s.songId);
});

test("estimated features: declared tempo and a hollow predicted energy, each with its basis in words", async ({ page }) => {
  await page.goto(`/songs/${songs[0]!.songId}`);
  const strip = page.getByTestId("song-features");
  await expect(strip).toBeVisible();
  await expect(page.getByTestId("tempo-feature")).toContainText("100 BPM");
  await expect(page.getByTestId("tempo-feature")).toContainText("declared");
  const energy = strip.getByTestId("energy-badge");
  await expect(energy).toHaveAttribute("data-basis", "predicted");
  await expect(energy).toContainText("predicted");
  await expect(strip.getByTestId("key-badge")).toHaveCount(0); // no key without a measurement
  await expect(page.getByTestId("features-pending")).toHaveCount(0);
});

test("measured and pending features render as the server sends them", async ({ page }) => {
  const id = songs[1]!.songId;
  let mode: "measured" | "pending" = "measured";
  await page.route(new RegExp(`/api/songs/${id}(\\?|$)`), async (route) => {
    const res = await route.fetch();
    const body = (await res.json()) as { features: Record<string, unknown> };
    if (mode === "measured") {
      body.features = { ...body.features, pending: false, tempo: { bpm: 121.5, basis: "measured", requested_bpm: 120, measured_bpm: 121.5, octave: false }, energy: { value: 0.62, basis: "measured" }, key: { camelot: "8A", name: "A minor", basis: "measured" } };
    } else body.features = { ...body.features, pending: true };
    await route.fulfill({ response: res, json: body });
  });
  await page.goto(`/songs/${id}`);
  await expect(page.getByTestId("tempo-feature")).toContainText("measured");
  await expect(page.getByTestId("energy-badge")).toHaveAttribute("data-basis", "measured");
  await expect(page.getByTestId("key-badge")).toContainText("8A");
  await expect(page.getByTestId("key-badge")).toContainText("A minor");
  await expect(page.getByTestId("features-pending")).toHaveCount(0);

  mode = "pending";
  await page.reload();
  await expect(page.getByTestId("features-pending")).toBeVisible();
});

test("Show on the Atlas opens the Song's neighbourhood; the Muse's link opens its Songs", async ({ page }) => {
  await page.goto(`/songs/${songs[0]!.songId}`);
  await page.getByTestId("song-atlas-link").click();
  await expect(page).toHaveURL(new RegExp(`/atlas\\?near=${songs[0]!.songId}`));
  await expect(page.getByTestId("atlas-near-pill")).toBeVisible();
  await expect(page.getByTestId("atlas-neighbours")).toBeVisible();

  await page.goto(`/muses/${museId}`);
  await page.getByTestId("muse-atlas-link").click();
  await expect(page).toHaveURL(new RegExp(`/atlas\\?muse=${museId}`));
  await expect(page.getByTestId("atlas-muse-pill")).toBeVisible();
});

test("the Song and Muse What-next lists are tappable", async ({ page }) => {
  // the Muse owns two rendered Songs nobody judged: its list says so and the item opens the Muse
  await page.goto(`/muses/${museId}`);
  const mine = page.getByTestId("muse-next");
  await expect(mine).toBeVisible();
  const item = mine.getByTestId("muse-next-item").first();
  await expect(item).toBeVisible();
  const link = item.getByRole("link");
  await expect(link).toHaveAttribute("href", /^\/(muses|songs|records|board)\//);

  // a Song without a Muse gets "link a Muse"-style items for the agent only; a human sees the Song's own items
  await page.goto(`/songs/${songs[0]!.songId}`);
  const list = page.getByTestId("song-next");
  await expect(list).toBeVisible();
  await expect(list.getByTestId("song-next-empty").or(list.getByTestId("song-next-item").first())).toBeVisible();
});
