// matrix: @firefox because the capability matrix is a plain table with horizontal scrolling on Firefox too
import { expect, test, type Page } from "./fixtures";
import { openNav } from "./helpers";
import { aceRow, adoptEngine, rejectEngine, watchE2 } from "./e2Api";

// This spec's server runs the committed features file (every task proved by the E2-X4 canary, 2026-11-17: specEnv in
// fixtures.ts), so the matrix shows what the deployment shows. The "not yet verified" label is pinned by engineMatrix.test.ts.
// The tests run in order: the
// engine is not adopted first (a retry starts from the operator's rejection), then adopted.
test.describe.configure({ mode: "serial", timeout: 90_000 });

/** The ACE-Step cell (second column) of a matrix row. */
const cellOf = (page: Page, row: string) => page.getByTestId(`matrix-row-${row}`).locator("td").nth(1);

test("the matrix lists the capabilities per engine, all verified by the canary", async ({ page, request, server }) => {
  if ((await aceRow(request)).adoption.state === "adopted") rejectEngine(server);
  const watch = watchE2(page);

  const row = await aceRow(request);
  expect(row.features?.tasks).toEqual(["complete", "extend", "extract", "repaint", "text2music"]);
  expect(row.features?.unverified).toEqual([]);

  await page.goto("/");
  await openNav(page, "Engines");
  await expect(page).toHaveURL(/\/engines$/);
  await expect(page.getByRole("heading", { name: "Engines" })).toBeVisible();
  const cols = page.getByTestId("engine-col");
  await expect(cols).toHaveCount(2);
  await expect(cols.nth(0)).toContainText("YuE2");
  await expect(cols.nth(0)).toContainText("(default)");
  await expect(cols.nth(1)).toContainText("ACE-Step");

  for (const k of ["text2song", "tempo", "key"]) await expect(cellOf(page, k)).toHaveAttribute("data-kind", "yes");
  for (const k of ["reference", "repaint", "extend", "complete", "stems", "lrc"]) await expect(cellOf(page, k)).toHaveAttribute("data-kind", "yes");
  await expect(cellOf(page, "reference")).toHaveText("up to 30 s");
  await expect(cellOf(page, "repaint")).toHaveText("3–90 s");
  await expect(cellOf(page, "extend")).toHaveText("3–90 s");
  await expect(cellOf(page, "max_duration")).toHaveText("480 s");

  // adoption and backend state (the engine is registered with its fake backend, armed, and not adopted)
  await expect(page.getByTestId("matrix-adoption").locator("td").nth(0)).toContainText("Adopted");
  await expect(page.getByTestId("matrix-adoption").locator("td").nth(1)).toContainText("Not adopted");
  await expect(page.getByTestId("matrix-adoption").locator("td").nth(1)).toContainText("engines adopt");
  await expect(page.getByTestId("matrix-backend").locator("td").nth(1)).toContainText(/^Armed, cold start \d+ s$/);

  // read-only in E2a: nothing on the page changes anything
  await expect(page.getByRole("main").locator("input, select, textarea, [data-testid=engine-matrix] button")).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});

test("adoption by the operator shows on the page; the capabilities do not change", async ({ page, request, server }) => {
  adoptEngine(server);
  expect((await aceRow(request)).adoption.state).toBe("adopted");
  const watch = watchE2(page);
  await page.goto("/engines");
  await expect(page.getByTestId("matrix-adoption").locator("td").nth(1)).toContainText("Adopted (cli)");
  await expect(page.getByTestId("matrix-adoption").locator("td").nth(1)).not.toContainText("engines adopt");
  await expect(cellOf(page, "repaint")).toHaveText("3–90 s");
  expect(watch.problems()).toEqual([]);
});
