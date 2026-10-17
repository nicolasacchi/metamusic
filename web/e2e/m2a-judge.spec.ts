// matrix: @mobile @realtime because rating and tagging happen from the player bar, whose controls must not overflow a phone viewport
import { expect, test, type APIRequestContext, type Page } from "./fixtures";
import { apiJSON, openNav, ownTracks, tap, uniqTag } from "./helpers";

// The tests share the project's seed track pair, so they run one after the other.
test.describe.configure({ mode: "serial" });

type Detail = { rating: { human: { stars: number } | null }; status: string; tags: { name: string }[] };
const detail = (request: APIRequestContext, id: string) => apiJSON<Detail>(request, "GET", `/api/tracks/${id}`);

// Leave the shared library as found: ratings and statuses are visible to other specs.
async function reset(request: APIRequestContext, id: string, tags: string[]) {
  await apiJSON(request, "PUT", `/api/tracks/${id}/rating`, { stars: null });
  await apiJSON(request, "PATCH", `/api/tracks/${id}`, { status: "draft" });
  if (tags.length > 0) await apiJSON(request, "PATCH", `/api/tracks/${id}/tags`, { remove: tags });
}

// The desktop player bar carries the judging controls; the phone mini-player does not (they live on the track page).
const isPhone = (page: Page) => (page.viewportSize()?.width ?? 1280) < 768;
const judgeBar = (page: Page) => page.getByTestId(isPhone(page) ? "track-judge" : "player-judge");

test("rate 4 and set to_relisten from the player bar (on a phone: from the track page)", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  try {
    await page.goto(`/tracks/${t.id}`);
    await page.getByTestId("track-play").click();
    const bar = judgeBar(page);
    await expect(bar).toBeVisible();
    await tap(page, bar.getByRole("button", { name: "4 stars", exact: true }));
    await expect(bar.getByRole("button", { name: "4 stars", exact: true })).toHaveAttribute("aria-pressed", "true");
    await bar.getByTestId("status-select").selectOption("to_relisten");
    await expect.poll(async () => (await detail(request, t.id)).rating.human?.stars).toBe(4);
    await expect.poll(async () => (await detail(request, t.id)).status).toBe("to_relisten");
    // the track page shows the same state without a reload
    await expect(page.getByTestId("track-judge").getByRole("button", { name: "4 stars", exact: true })).toHaveAttribute("aria-pressed", "true");
    await page.reload();
    await expect(page.getByTestId("track-judge").getByTestId("status-select")).toHaveValue("to_relisten");
  } finally {
    await reset(request, t.id, []);
  }
});

test("the player bar with its judging controls never overflows the viewport", async ({ page, request }, info) => {
  const [t] = await ownTracks(request, info);
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect(page.getByTestId("player-bar")).toBeVisible();
  // the visual viewport is what the user sees: overflow widens the layout viewport (innerWidth) instead
  const measure = () => page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: window.visualViewport?.width ?? window.innerWidth }));
  // client-side navigation keeps the player (and its judging controls) alive
  for (const where of ["track", "library", "backlog"]) {
    if (where === "library") await openNav(page, "Library");
    if (where === "backlog") await openNav(page, "Backlog");
    await expect(page.getByTestId("player-bar")).toBeVisible();
    if (isPhone(page)) expect((await page.getByTestId("player-footer").boundingBox())?.height ?? 0).toBeLessThanOrEqual(64);
    else await expect(page.getByTestId("player-judge")).toBeVisible();
    const m = await measure();
    expect(m.sw, `scrollWidth ${m.sw} > visual viewport ${m.iw} on ${where}`).toBeLessThanOrEqual(m.iw);
  }
});

test("tags are added from suggestions and removed", async ({ page, request }, info) => {
  const [t, other] = await ownTracks(request, info);
  const known = uniqTag("jt-known");
  const fresh = uniqTag("jt-new");
  // a tag that exists on another track must be offered as a suggestion (list_tags)
  await apiJSON(request, "PATCH", `/api/tracks/${other.id}/tags`, { add: [known] });
  try {
    await page.goto(`/tracks/${t.id}`);
    const editor = page.getByTestId("tag-editor");
    await expect(editor).toBeVisible();
    await expect(editor.locator(`datalist option[value="${known}"]`)).toHaveCount(1);
    for (const name of [known, fresh]) {
      await editor.getByLabel("Add tag").fill(name);
      await editor.getByRole("button", { name: "Add", exact: true }).click();
      await expect(editor.getByRole("button", { name: `Remove tag ${name}` })).toBeVisible();
    }
    await expect.poll(async () => (await detail(request, t.id)).tags.map((x) => x.name).sort()).toEqual([fresh, known].sort());
    await editor.getByRole("button", { name: `Remove tag ${fresh}` }).click();
    await expect(editor.getByRole("button", { name: `Remove tag ${fresh}` })).toHaveCount(0);
    await expect.poll(async () => (await detail(request, t.id)).tags.map((x) => x.name)).toEqual([known]);
  } finally {
    await reset(request, t.id, [known, fresh]);
    await apiJSON(request, "PATCH", `/api/tracks/${other.id}/tags`, { remove: [known] });
  }
});

test("library filters: status, two tags AND, min stars, back/forward", async ({ page, request }, info) => {
  const [t, other] = await ownTracks(request, info);
  const a = uniqTag("jt-a");
  const b = uniqTag("jt-b");
  // Both tracks are to_relisten and carry tag a; only t carries tag b and 4 stars. (Other specs may rate
  // `other`, so nothing here depends on its stars.)
  await apiJSON(request, "PATCH", `/api/tracks/${t.id}/tags`, { add: [a, b] });
  await apiJSON(request, "PATCH", `/api/tracks/${other.id}/tags`, { add: [a] });
  await apiJSON(request, "PUT", `/api/tracks/${t.id}/rating`, { stars: 4 });
  for (const id of [t.id, other.id]) await apiJSON(request, "PATCH", `/api/tracks/${id}`, { status: "to_relisten" });
  const search = (r: { url(): string }) => new URL(r.url()).pathname === "/api/tracks";
  const params = (r: { url(): string }) => new URL(r.url()).searchParams;
  try {
    await page.goto("/");
    await expect(page.getByTestId("track-row").first()).toBeVisible();

    const status = page.waitForRequest((r) => search(r) && params(r).getAll("status").includes("to_relisten"));
    await page.getByRole("group", { name: "Status" }).getByLabel("to_relisten").check();
    await status;

    const facets = page.getByTestId("tag-facets");
    const first = page.waitForRequest((r) => search(r) && params(r).getAll("tag").join() === a);
    await facets.getByRole("button", { name: new RegExp(`^${a}\\b`) }).click();
    await first;
    await expect(page.getByTestId("track-row")).toHaveCount(2);

    const both = page.waitForRequest((r) => search(r) && r.url().includes(`tag=${a}&tag=${b}`));
    await facets.getByRole("button", { name: new RegExp(`^${b}\\b`) }).click();
    await both;
    // the rows are exactly the AND of both tags
    await expect(page.getByTestId("track-row")).toHaveCount(1);
    await expect(page.getByTestId("track-row").first()).toHaveAttribute("data-track-id", t.id);

    const stars = page.waitForRequest((r) => search(r) && params(r).get("min_stars") === "4" && params(r).getAll("tag").join() === `${a},${b}`);
    await page.getByTestId("filter-stars").selectOption("4");
    await stars;
    await expect(page.getByTestId("track-row")).toHaveCount(1);
    await expect(page.getByTestId("row-stars").first()).toHaveAttribute("aria-label", "4 stars");

    // back drops the stars filter, forward restores it
    await page.goBack();
    await expect(page).toHaveURL((u) => !u.searchParams.has("min_stars") && u.searchParams.getAll("tag").join() === `${a},${b}`);
    await expect(page.getByTestId("filter-stars")).toHaveValue("");
    await page.goBack();
    await expect(page).toHaveURL((u) => u.searchParams.getAll("tag").join() === a);
    await expect(page.getByTestId("track-row")).toHaveCount(2);
    await page.goForward();
    await page.goForward();
    await expect(page).toHaveURL((u) => u.searchParams.get("min_stars") === "4" && u.searchParams.getAll("tag").join() === `${a},${b}`);
    await expect(page.getByTestId("filter-stars")).toHaveValue("4");
    await expect(page.getByTestId("track-row")).toHaveCount(1);
  } finally {
    await reset(request, t.id, [a, b]);
    await reset(request, other.id, [a]).catch(() => {});
  }
});
