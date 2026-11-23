// matrix: @mobile because the night shift is read, halted and revoked from the phone, and the secure forms are opened from the More tab
// M6N-W1 (m6-night C388): the mandate pages, the night-shift page and the two secure forms, against the real server with
// the fake backend. Nothing is stubbed: every operation below is answered by the Go handlers and counted by the served check.
// The Owner runs it end to end; an agent token must not reach the secure forms. (Running a night through a test hook is not
// possible yet: the e2e server has no such hook, so the night itself is pinned by TestAcceptance_M6N_SimulatedNight.)
import { expect as baseExpect, test, type Page } from "./fixtures";
import { agentContext } from "./l2Api";

const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 150_000 });

/** datetime-local text (local time) `days` from now. */
function local(days: number): string {
  const d = new Date(Date.now() + days * 86_400_000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

async function openMore(page: Page, label: string) {
  await page.goto("/more");
  await page.getByRole("main").getByRole("link", { name: new RegExp(`^${label}\\b`) }).click();
}

test("the night shift page says plainly that the night is off, and what the first night should be", async ({ page }) => {
  await openMore(page, "Night shift");
  await expect(page).toHaveURL(/\/night-shift$/);
  await expect(page.getByRole("heading", { level: 1, name: "Night shift" })).toBeVisible();
  const off = page.getByTestId("night-off-notice");
  await expect(off).toContainText("off until the Owner enables it");
  await expect(off).toContainText("live mandate");
  await expect(off).toContainText("$1.00 per night");
  await expect(off).toContainText("01:30");
  await expect(page.getByTestId("night-attended-notice")).toContainText("first night attended, during the day");
  await expect(page.getByTestId("night-status")).toHaveAttribute("data-tone", "off");
  await expect(page.getByTestId("night-authority")).toContainText("none");
  // the preview (get_night_shift's twin) answers with a plan or the reason tonight would be skipped, never an error
  await expect(page.getByTestId("night-plan")).toBeVisible();
  await expect(page.getByTestId("night-plan-error")).toHaveCount(0);
  await expect(page.getByTestId("night-runs-empty")).toBeVisible();
});

test("the mandates page starts empty and links to the secure form", async ({ page }) => {
  await openMore(page, "Mandates");
  await expect(page.getByTestId("mandates-empty")).toBeVisible();
  await expect(page.getByTestId("mandate-new-link")).toHaveAttribute("href", "/human/mandates/new");
});

test("enabling the night shift without a live mandate is refused and the page stays off", async ({ page }) => {
  await page.goto("/human/night-shift");
  await page.getByTestId("night-enable").click();
  await expect(page.getByTestId("night-settings-error")).toContainText(/live spending mandate/);
  await expect(page.getByTestId("night-settings-status")).toContainText("Off");
  await expect(page.getByTestId("night-enable")).toBeVisible();
});

test("the secure form creates a mandate: nothing is prefilled, a bad figure is refused, then it is created", async ({ page }) => {
  await page.goto("/human/mandates/new");
  await expect(page.getByTestId("human-mandate-new-page")).toBeVisible();
  await expect(page.getByTestId("mandate-budget")).toHaveValue("");
  await expect(page.getByTestId("mandate-ceiling")).toHaveValue("");
  await expect(page.getByTestId("human-mandate-new-page")).toContainText("never raises a cap");
  await page.getByTestId("mandate-title").fill("First week");
  await page.getByTestId("mandate-budget").fill("1");
  await page.getByTestId("mandate-ceiling").fill("2"); // above the budget
  await page.getByTestId("mandate-until").fill(local(2));
  await page.getByTestId("mandate-create").click();
  await expect(page.getByTestId("mandate-form-error")).toContainText("cannot be above the budget");
  await page.getByTestId("mandate-ceiling").fill("0.5");
  await page.getByTestId("mandate-create").click();
  await expect(page).toHaveURL(/\/mandates\/[^/]+$/);
  await expect(page.getByTestId("mandate-page").getByRole("heading", { level: 1, name: "First week" })).toBeVisible();
  await expect(page.getByTestId("mandate-state")).toHaveText("Live");
  await expect(page.getByTestId("mandate-usage")).toContainText("$1.00");
  await expect(page.getByTestId("mandate-usage-bar")).toBeVisible();
});

test("the list shows the live mandate", async ({ page }) => {
  await page.goto("/mandates");
  const row = page.getByTestId("mandate-row");
  await expect(row).toHaveCount(1);
  await expect(row).toHaveAttribute("data-state", "live");
  await expect(row).toContainText("First week");
  await expect(row.getByTestId("mandate-revoke-open")).toBeVisible();
});

test("the secure settings enable the night shift, and the page shows the mandate as its authority", async ({ page }) => {
  await page.goto("/night-shift");
  await page.getByTestId("night-settings-link").click();
  await expect(page).toHaveURL(/\/human\/night-shift$/);
  await expect(page.getByTestId("human-night-shift-page")).toContainText("off until the Owner enables it");
  await page.getByTestId("night-at").fill("25:99");
  await page.getByTestId("night-enable").click();
  await expect(page.getByTestId("night-settings-error")).toContainText("24-hour");
  await page.getByTestId("night-at").fill("01:30");
  await page.getByTestId("night-enable").click();
  await expect(page.getByTestId("night-settings-status")).toContainText("On");
  await expect(page.getByTestId("night-disable")).toBeVisible();
  await page.goto("/night-shift");
  await expect(page.getByTestId("night-status")).toHaveAttribute("data-tone", "on");
  await expect(page.getByTestId("night-authority")).toContainText("First week");
  await expect(page.getByTestId("night-off-notice")).toHaveCount(0);
  await expect(page.getByTestId("night-attended-notice")).toBeVisible();
  await expect(page.getByTestId("night-next")).not.toHaveText("not scheduled");
});

test("a halt needs a reason and stops the night shift", async ({ page }) => {
  await page.goto("/night-shift");
  await page.getByTestId("night-halt").click();
  await expect(page.getByTestId("night-halt-error")).toContainText("reason is required");
  await page.getByTestId("night-halt-reason").fill("checking the halt");
  await page.getByTestId("night-halt").click();
  await expect(page.getByTestId("night-status")).toHaveAttribute("data-tone", "halted");
  await expect(page.getByTestId("night-status")).toContainText("checking the halt");
});

test("a mandate is revoked with a reason", async ({ page }) => {
  await page.goto("/mandates");
  await page.getByTestId("mandate-row").getByRole("link", { name: "First week" }).click();
  await expect(page).toHaveURL(/\/mandates\/[^/]+$/);
  await page.getByTestId("mandate-revoke-open").click();
  await page.getByTestId("mandate-revoke-confirm").click();
  await expect(page.getByTestId("mandate-revoke-error")).toContainText("reason is required");
  await page.getByTestId("mandate-revoke-reason").fill("done with the test");
  await page.getByTestId("mandate-revoke-confirm").click();
  await expect(page.getByTestId("mandate-state")).toHaveText("Revoked");
  await expect(page.getByTestId("mandate-revoked")).toContainText("done with the test");
  await page.goto("/mandates");
  await expect(page.getByTestId("mandate-row")).toHaveAttribute("data-state", "revoked");
  await expect(page.getByTestId("mandate-revoke-open")).toHaveCount(0);
});

test("an agent token can neither create a mandate nor change the night shift", async ({ playwright, baseURL, agentToken }) => {
  const agent = await agentContext(playwright, baseURL, agentToken);
  // control: the same token is valid and may read, so the refusals below come from the human-only rule, not a bad token
  expect((await agent.get("/api/night-shift")).status()).toBe(200);
  const create = await agent.post("/human/api/mandates", { headers: { "Idempotency-Key": "agent-try-1" }, data: { holder: "night_shift", title: "x", budget_micro_usd: 1_000_000, valid_until: new Date(Date.now() + 86_400_000).toISOString(), scope: { modes: ["new_seed"], engines: ["yue2"], min_stars: 4, max_takes_per_lineage: 1, max_takes_per_batch: 1, max_batch_ceiling_micro_usd: 500_000, max_batches_per_day: 1, tier_b: false } } });
  expect(create.status()).toBe(403);
  expect((await create.json()).code).toBe("HUMAN_ONLY");
  const update = await agent.put("/human/api/night-shift", { headers: { "Idempotency-Key": "agent-try-2" }, data: { enabled: true } });
  expect(update.status()).toBe(403);
  expect((await update.json()).code).toBe("HUMAN_ONLY");
  await agent.dispose();
});
