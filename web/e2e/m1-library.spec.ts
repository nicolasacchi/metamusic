// matrix: @mobile because the library is the main phone page (filters, facets, URL restore on a narrow layout)
import { expect, test, type APIRequestContext } from "@playwright/test";
import { SEED_RECIPE_TRACKS, SEED_TRACKS } from "./env";

const TITLES = ["Syn ears clip", "Syn ears gap", "Syn ears loop", "Syn song 01 s1001", "Syn song 01 s2001", "Syn song 02", "Syn song 03", "Syn song 04", "Syn song 05"];

async function listTitles(request: APIRequestContext): Promise<string[]> {
  const r = await request.get("/api/tracks?limit=50&recipe_known=true");
  expect(r.ok()).toBeTruthy();
  return ((await r.json()) as { items: { title: string }[] }).items.map((t) => t.title);
}

test("seeded titles are listed", async ({ page, request }) => {
  expect((await listTitles(request)).sort()).toEqual(TITLES);
  await page.goto("/");
  const rows = page.getByTestId("track-row");
  expect(TITLES).toHaveLength(SEED_RECIPE_TRACKS);
  await expect(rows).toHaveCount(SEED_TRACKS);
  for (const t of TITLES) await expect(page.getByTestId("track-link").filter({ hasText: new RegExp(`^${t}$`) })).toBeVisible();
  await expect(page.getByTestId("library-counts")).toContainText(`${SEED_TRACKS} tracks`);
});

test("q search is accent-insensitive", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByTestId("track-row")).toHaveCount(SEED_TRACKS);
  await page.getByTestId("filter-q").fill("Sýn sóng 03");
  await expect(page).toHaveURL(/[?&]q=/);
  await expect(page.getByTestId("track-row")).toHaveCount(1);
  await expect(page.getByTestId("track-link")).toHaveText("Syn song 03");
});

test("two style facets select the AND of both terms", async ({ page }) => {
  // The seeded library has a single style term; a second facet is injected into the facets
  // response so the AND semantics (and the repeated query parameter) can be exercised.
  await page.route("**/api/library/facets", async (route) => {
    const res = await route.fetch();
    const body = (await res.json()) as { style_terms: { term: string; count: number }[] };
    body.style_terms.push({ term: "ambient", count: 1 });
    await route.fulfill({ response: res, json: body });
  });
  await page.goto("/?recipe_known=true");
  await expect(page.getByTestId("track-row")).toHaveCount(SEED_RECIPE_TRACKS);
  const facets = page.getByTestId("style-facets");

  await facets.getByRole("button", { name: /^synthetic pop/ }).click();
  await expect(facets.getByRole("button", { name: /^synthetic pop/ })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByTestId("track-row")).toHaveCount(SEED_RECIPE_TRACKS);

  const req = page.waitForRequest((r) => {
    const u = new URL(r.url());
    return u.pathname === "/api/tracks" && u.searchParams.getAll("style").length === 2;
  });
  await facets.getByRole("button", { name: /^ambient/ }).click();
  const u = new URL((await req).url());
  expect(u.searchParams.getAll("style")).toEqual(["synthetic pop", "ambient"]);
  expect(u.search).toMatch(/style=synthetic(\+|%20)pop&style=ambient/);
  // No track carries both terms.
  await expect(page.getByTestId("track-row")).toHaveCount(0);
  await expect(page.getByText("No tracks match.")).toBeVisible();
});

test("duration filter and URL restore on back/forward", async ({ page }) => {
  await page.goto("/?recipe_known=true");
  await expect(page.getByTestId("track-row")).toHaveCount(SEED_RECIPE_TRACKS);

  await page.getByTestId("style-facets").getByRole("button", { name: /^synthetic pop/ }).click();
  await expect(page).toHaveURL(/style=synthetic/);

  // minimum length is entered in minutes: 0.14 min = 8.4 s keeps the 9 s, 10 s and two 12 s takes
  await page.locator('input[name="min"]').fill("0.14");
  await page.locator('input[name="min"]').press("Tab");
  await expect(page).toHaveURL(/min_duration_s=8\.3|min_duration_s=8\.4/);
  await expect(page.getByTestId("track-row")).toHaveCount(4);
  const names = await page.getByTestId("track-link").allTextContents();
  expect(names.sort()).toEqual(["Syn ears gap", "Syn ears loop", "Syn song 03", "Syn song 05"]);

  await page.goBack();
  await expect(page).toHaveURL(/style=synthetic/);
  await expect(page).not.toHaveURL(/min_duration_s/);
  await expect(page.getByTestId("track-row")).toHaveCount(SEED_RECIPE_TRACKS);
  await expect(page.locator('input[name="min"]')).toHaveValue("");

  await page.goForward();
  await expect(page).toHaveURL(/min_duration_s/);
  await expect(page.getByTestId("track-row")).toHaveCount(4);
  await expect(page.locator('input[name="min"]')).toHaveValue("0.14");

  // a deep link restores the same state in a fresh load
  await page.reload();
  await expect(page.getByTestId("track-row")).toHaveCount(4);
  await expect(page.getByTestId("style-facets").getByRole("button", { name: /^synthetic pop/ })).toHaveAttribute("aria-pressed", "true");
});
