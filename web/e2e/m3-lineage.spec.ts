import { expect, test } from "@playwright/test";
import { apiJSON } from "./helpers";
import { composeRoots, first, newSeedChildren } from "./m3Api";

// Desktop Chrome only. The spec composes its own root and two children, so the tree it renders is exactly three nodes.

test.describe.configure({ timeout: 150_000 });

test("the lineage tree opens a non-blind duel with the parent and the compare page", async ({ page, request }) => {
  const root = first(await composeRoots(request, 1, "m3l"));
  const kids = (await newSeedChildren(request, root, 2)).map((j) => j.track_id ?? "");
  expect(kids.every((k) => k !== "")).toBe(true);

  await page.goto(`/lineage/${root}`);
  await expect(page.getByTestId("lineage-count")).toContainText("3 of 3 tracks");
  const nodes = page.getByTestId("lineage-node");
  await expect(nodes).toHaveCount(3);
  await expect(page.locator(`[data-testid="lineage-node"][data-id="${root}"]`)).toHaveClass(/current/);
  // the root has no parent, so it offers neither the duel nor the compare
  await expect(page.locator(`[data-testid="lineage-node"][data-id="${root}"]`).getByTestId("lineage-ab-parent")).toHaveCount(0);

  const child = page.locator(`[data-testid="lineage-node"][data-id="${kids[0]}"]`);
  await expect(child.getByTestId("lineage-operator")).toContainText("reroll seed");
  await child.getByTestId("lineage-ab-parent").click();
  await expect(page).toHaveURL(/\/duels\/[0-9a-f-]+$/);
  const duelId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const duel = await apiJSON<{ blind: boolean; slots: { a: { track: { id: string } | null }; b: { track: { id: string } | null } } }>(request, "GET", `/api/duels/${duelId}`);
  expect(duel.blind).toBe(false);
  expect([duel.slots.a.track?.id, duel.slots.b.track?.id].sort()).toEqual([root, kids[0]].sort());

  await page.goto(`/lineage/${root}`);
  await page.locator(`[data-testid="lineage-node"][data-id="${kids[1]}"]`).getByTestId("lineage-compare").click();
  await expect(page).toHaveURL(new RegExp(`/compare\\?a=${root}&b=${kids[1]}$`));
  await expect(page.getByTestId("relation")).toBeVisible({ timeout: 15_000 });
});
