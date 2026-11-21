// matrix: chrome-only because it is a server-side data flow (estimate, confirm, job, stored check); the panel is the shared Track card
import { expect, test } from "./fixtures";
import { ownTracks } from "./helpers";

// The listening check (M5b W1): the Analyze button goes through the ordinary estimate and confirmation, the panel then shows what was
// heard (get_track_tier_b), a closed worker image is explained before any click, and the estimate offers to skip the check.
// Each project owns its own pair of seed tracks (ownTracks), and the fake backend has every capability open.

test.describe.configure({ timeout: 120_000 });

test("Analyze estimates and confirms a listening check, then the panel shows what was heard", async ({ page, request }, info) => {
  const [mine] = await ownTracks(request, info);
  await page.goto(`/tracks/${mine.id}`);
  const panel = page.getByTestId("tierb-panel");
  await expect(panel.getByTestId("tierb-state")).toContainText("No listening check yet");

  // never a job from the button itself: it lands on the estimate, which is the price and the confirmation
  await panel.getByTestId("analyze-button").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("intent-summary")).toContainText("listening check of 1 take");
  await expect(page.getByTestId("estimate")).toBeVisible();
  await expect(page.getByTestId("tierb-summary")).toContainText("Listens to the vocals of 1 take");
  await expect(page.getByTestId("tierb-cost")).toContainText("The listening check is the whole job: up to $");
  await expect(page.getByTestId("skip-tierb")).toHaveCount(0);
  await expect(page.getByTestId("take-check")).toHaveAttribute("data-on", "true");
  await expect(page.getByTestId("confirm-button")).toContainText("for the listening check");

  await page.getByTestId("confirm-button").click();
  await expect(page).toHaveURL(/\/batches\/[^/]+$/);

  // the fake worker answers the check; GET never mutates, so poll the read until it exists
  await expect
    .poll(async () => (await request.get(`/api/tracks/${mine.id}/tier-b`)).status(), { timeout: 60_000, intervals: [500, 1000] })
    .toBe(200);

  await page.goto(`/tracks/${mine.id}`);
  const state = panel.getByTestId("tierb-state");
  await expect(state).toHaveAttribute("data-state", "ok");
  await expect(state).toContainText("Listened to the vocals");
  await expect(panel.getByTestId("tierb-measured")).toBeVisible();
  await expect(panel.getByTestId("tierb-adherence")).toContainText("Melody");
  await expect(panel.getByTestId("tierb-models")).toContainText("Heard with");
  await expect(panel.getByRole("heading", { name: "What this cannot hear" })).toBeVisible();
  await expect(panel.getByTestId("analyze-button")).toHaveCount(0);
});

test("with the worker image closed for it, Analyze is disabled and says why", async ({ page, request }, info) => {
  const [, other] = await ownTracks(request, info);
  // the real status, with the ears and analyze capabilities taken out
  await page.route("**/api/provider", async (route) => {
    const res = await route.fetch();
    const body = (await res.json()) as { worker: { caps: string[] } };
    body.worker.caps = ["v2"];
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto(`/tracks/${other.id}`);
  const panel = page.getByTestId("tierb-panel");
  await expect(panel.getByTestId("analyze-unavailable")).toContainText("the worker image is not open for it (missing: ears, analyze)");
  await expect(panel.getByTestId("analyze-button")).toBeDisabled();
  await expect(page).toHaveURL(new RegExp(`/tracks/${other.id}$`));
});

test("a render estimate shows the check per take, what it costs, and Skip re-estimates without it", async ({ page, request }, info) => {
  const [, other] = await ownTracks(request, info);
  await page.goto(`/tracks/${other.id}`);
  await page.getByTestId("generate-button").locator("summary").click();
  await page.getByTestId("variant-new_seed").click();
  await expect(page).toHaveURL(/\/human\/generate$/);
  await expect(page.getByTestId("estimate")).toBeVisible();
  await expect(page.getByTestId("tierb-summary")).toContainText("get a listening check after the render");
  const badges = page.getByTestId("take-check");
  await expect(badges.first()).toHaveAttribute("data-on", "true");
  await expect(page.getByTestId("tierb-cost")).toContainText("The listening check adds up to $");
  await expect(page.getByTestId("skip-tierb")).not.toBeChecked();

  const skipped = page.waitForRequest((r) => r.url().endsWith("/api/generations/estimate") && (r.postData() ?? "").includes('"tier_b":false'));
  await page.getByTestId("skip-tierb").check();
  await skipped;
  await expect(page.getByTestId("tierb-summary")).toContainText("Skipped: no take gets a listening check.");
  await expect(page.getByTestId("skip-tierb")).toBeChecked();
  await expect(page.getByTestId("take-check").first()).toHaveAttribute("data-on", "false");
  await expect(page.getByTestId("tierb-cost")).toHaveCount(0);
});
