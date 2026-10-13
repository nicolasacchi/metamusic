import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
import { trackByTitle, uniq } from "./helpers";
import { id3Frames, readZip } from "./zip";

test("playlist: create, add from track pages, reorder, play all, export a ZIP of tagged MP3s", async ({ page, request }) => {
  const name = uniq("e2e-list");
  const a = await trackByTitle(request, "Syn song 02");
  const b = await trackByTitle(request, "Syn song 04");

  await page.goto("/playlists");
  await page.getByTestId("playlist-name").fill(name);
  await page.getByTestId("playlist-create").click();
  const link = page.getByTestId("playlist-list").getByRole("link", { name, exact: true });
  await expect(link).toBeVisible();

  for (const t of [a, b]) {
    await page.goto(`/tracks/${t.id}`);
    const add = page.getByTestId("add-to-playlist");
    await expect(add.getByTestId("playlist-select")).toBeVisible();
    await add.getByTestId("playlist-select").selectOption({ label: name });
    await add.getByTestId("playlist-add").click();
    await expect(add.getByTestId("in-playlists")).toContainText(name);
  }

  await page.goto("/playlists");
  await link.click();
  await expect(page.getByTestId("playlist-title")).toHaveText(name);
  const items = page.getByTestId("playlist-items").locator("li");
  await expect(items).toHaveCount(2);
  await expect(items.nth(0)).toContainText("Syn song 02");

  await page.getByRole("button", { name: "Move Syn song 04 up" }).click();
  await expect(items.nth(0)).toContainText("Syn song 04");
  await page.reload();
  await expect(items.nth(0)).toContainText("Syn song 04");
  await expect(items.nth(1)).toContainText("Syn song 02");

  await page.getByTestId("playlist-play-all").click();
  await expect(page.getByTestId("play-toggle")).toHaveAttribute("aria-label", "Pause");
  await expect(page.getByTestId("player-bar").getByRole("link", { name: "Syn song 04" })).toBeVisible();

  await page.getByTestId("playlist-zip").click();
  const download = page.waitForEvent("download");
  await page.getByTestId("zip-download").click();
  const path = await (await download).path();
  const entries = readZip(readFileSync(path));
  const mp3 = entries.filter((e) => e.name.toLowerCase().endsWith(".mp3"));
  expect(mp3).toHaveLength(2);
  const titles = mp3.map((e) => id3Frames(e.data).TIT2);
  expect(titles).toEqual(["Syn song 04", "Syn song 02"]);
  for (const e of mp3) expect(id3Frames(e.data).TALB).toBe(name);
});
