// matrix: @mobile because the opt-in pages and the Library sub-view row (Records, Crates, Back restoring the list position) are phone surfaces
import { expect as baseExpect, test, type APIRequestContext } from "./fixtures";
import { apiJSON, openNav, uniq } from "./helpers";
import { agentContext, createRecord, curationSettings, retireRecord } from "./l2Api";

const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 120_000 });

const setBoth = (request: APIRequestContext, on: boolean) => apiJSON(request, "PUT", "/human/api/curation/settings", { export_muse_names: on, subsonic_muse_names: on });

test.beforeAll(async ({ request }) => {
  await setBoth(request, false); // each spec starts from the shipped defaults
});

test("the settings page shows both opt-ins off, read-only, with a link to the secure page", async ({ page, request }) => {
  expect(await curationSettings(request)).toMatchObject({ export_muse_names: false, subsonic_muse_names: false });
  await page.goto("/settings/curation");
  await expect(page.getByTestId("settings-curation-page")).toBeVisible();
  await expect(page.getByTestId("curation-toggle-export_muse_names")).toHaveText("Off");
  await expect(page.getByTestId("curation-toggle-subsonic_muse_names")).toHaveText("Off");
  await expect(page.getByRole("switch")).toHaveCount(0);
  await expect(page.getByTestId("curation-change-link")).toHaveAttribute("href", "/human/curation");
});

test("the secure page toggles one opt-in and the page reflects it; the other stays off", async ({ page, request }) => {
  await page.goto("/human/curation");
  await expect(page.getByTestId("human-curation-page")).toBeVisible();
  const sw = page.getByTestId("curation-toggle-export_muse_names");
  await expect(sw).toHaveAttribute("aria-checked", "false");
  await sw.click();
  await expect(sw).toHaveAttribute("aria-checked", "true");
  await expect.poll(async () => (await curationSettings(request)).export_muse_names).toBe(true);
  expect((await curationSettings(request)).subsonic_muse_names).toBe(false);
  await page.reload();
  await expect(page.getByTestId("curation-toggle-export_muse_names")).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("curation-toggle-subsonic_muse_names")).toHaveAttribute("aria-checked", "false");
  // the read-only settings page agrees
  await page.goto("/settings/curation");
  await expect(page.getByTestId("curation-toggle-export_muse_names")).toHaveText("On");
});

test("the opt-ins are human-only: an agent can read them but not change them", async ({ request, playwright, baseURL, agentToken }) => {
  const agent = await agentContext(playwright, baseURL, agentToken);
  const read = await agent.get("/api/curation/settings");
  expect(read.ok()).toBe(true);
  const write = await agent.put("/human/api/curation/settings", { data: { subsonic_muse_names: true } });
  expect(write.status()).toBe(403);
  await agent.dispose();
  expect((await curationSettings(request)).subsonic_muse_names).toBe(false);
});

test("the Library sub-view row shows Records and Crates, and Back restores the Records list position", async ({ page, request }) => {
  const made: string[] = [];
  const tag = uniq("l2 scroll");
  try {
    for (let i = 0; i < 16; i++) made.push((await createRecord(request, `${tag} ${String(i).padStart(2, "0")}`)).record.id);
    await page.setViewportSize({ width: 390, height: 500 });
    await page.goto("/library");
    const views = page.getByRole("navigation", { name: "Library views" });
    await expect(views.getByRole("link", { name: /^Records\b/ })).toBeVisible();
    await expect(views.getByRole("link", { name: /^Crates\b/ })).toBeVisible();
    await openNav(page, "Records");
    await expect(page).toHaveURL(/\/records$/);
    await page.getByRole("searchbox", { name: "Search records" }).fill(tag);
    await expect(page.getByTestId("record-row")).toHaveCount(16);

    const max = await page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight);
    expect(max, "the Records list is taller than the window").toBeGreaterThan(300);
    const target = Math.floor(max * 0.7);
    await page.evaluate((y) => window.scrollTo(0, y), target);
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(target - 5);
    await page.waitForTimeout(300); // the position is saved on a short debounce
    const link = await page.getByTestId("record-row").locator("a").evaluateAll((els) => {
      const mid = window.innerHeight / 2;
      const el = els.map((e) => ({ e, d: Math.abs(e.getBoundingClientRect().top - mid) })).sort((a, b) => a.d - b.d)[0]?.e;
      return el?.getAttribute("href") ?? "";
    });
    expect(link).toMatch(/^\/records\//);
    await page.locator(`[data-testid="record-row"] a[href="${link}"]`).click();
    await expect(page.getByTestId("record-studio")).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(/\/records$/);
    await expect.poll(() => page.evaluate(() => window.scrollY), { message: "Back restores the position" }).toBeGreaterThan(target - 60);
  } finally {
    for (const id of made) await retireRecord(request, id);
  }
});

test.afterAll(async ({ request }) => {
  await setBoth(request, false); // reset
});
