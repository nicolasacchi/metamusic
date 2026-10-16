import { expect, test } from "./fixtures";
import { apiJSON, trackByTitle, uniq } from "./helpers";

test("human mutations made here are listed and the filters narrow the list", async ({ page, request }) => {
  const name = uniq("e2e-audit");
  const made = await apiJSON<{ id: string }>(request, "POST", "/api/playlists", { name }, true);
  await apiJSON(request, "PATCH", `/api/playlists/${made.id}`, { revision: 1, hidden: true });

  const t = await trackByTitle(request, "Syn song 04");
  const comment = await apiJSON<{ id: string }>(request, "POST", `/api/tracks/${t.id}/comments`, { body: uniq("audit") }, true);

  await page.goto("/settings/audit");
  const table = page.getByTestId("audit-table");
  await page.getByTestId("audit-op").fill("create_playlist,update_playlist");
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(table.locator('tbody tr[data-op="create_playlist"]').first()).toBeVisible();
  const ops = await table.locator("tbody tr").evaluateAll((rows) => rows.map((r) => (r as HTMLElement).dataset.op));
  expect(ops.length).toBeGreaterThanOrEqual(2);
  expect(new Set(ops.filter(Boolean))).toEqual(new Set(["create_playlist", "update_playlist"]));

  // the comment's row names the new comment id (created_ids)
  await page.getByTestId("audit-op").fill("add_comment");
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(table.locator("tbody tr", { hasText: comment.id })).toHaveCount(1);
  await expect(table.locator("tbody tr", { hasText: comment.id })).toContainText("human");

  // narrowing to one op drops the other
  await page.getByTestId("audit-op").fill("update_playlist");
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(table.locator('tbody tr[data-op="update_playlist"]').first()).toBeVisible();
  await expect(table.locator('tbody tr[data-op="create_playlist"]')).toHaveCount(0);
  await expect(table.locator('tbody tr[data-op="update_playlist"]').first()).toBeVisible();

  // a status floor above every success leaves no row for these ops
  await page.getByTestId("audit-status-min").fill("400");
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(table.locator('tbody tr[data-op="update_playlist"]')).toHaveCount(0);
  await expect(table).toContainText("No matching entries");
});
