// matrix: @mobile because the notation and the bar editor are a phone-width page (touch targets, no horizontal overflow)
import type { APIRequestContext } from "@playwright/test";
import { expect, test, type Page } from "./fixtures";
import { allTracks, idemKey, smallTargets } from "./helpers";

// DESK-W1 (m5b-desk C363.2): the Desk pages. A take that carries a score is opened in the Desk from its own page, read as
// notation, edited through the bar editor and judged; the /desk list groups the plans and the score check reads pasted ABC.
// Each worker serves its own library, so a plan made here is seen by no other spec. Scores in this file are toy ABC.

const TOY = "X:1\nT:Toy\nM:4/4\nL:1/4\nQ:1/4=100\nK:C\nCDEF|G4|]\n";

/** A seed take the Desk can open: the first one whose plan the server makes (a YuE2 take with a score). */
async function takeWithScore(request: APIRequestContext): Promise<{ id: string; title: string }> {
  const tried: string[] = [];
  for (const t of (await allTracks(request)).filter((x) => x.title.startsWith("Syn song"))) {
    const r = await request.fetch(`/api/tracks/${t.id}/plan`, { method: "POST", headers: { "Idempotency-Key": idemKey() } });
    if (r.ok()) return t;
    tried.push(`${t.title}: ${r.status()} ${await r.text()}`);
  }
  throw new Error(`no seed take opens in the Desk:\n${tried.join("\n")}`);
}

async function openFromTake(page: Page, id: string) {
  await page.goto(`/tracks/${id}`);
  await page.getByTestId("open-in-desk-button").click();
  await expect(page).toHaveURL(/\/desk\/plans\/[^/]+$/);
  await expect(page.getByTestId("plan-title")).toBeVisible();
}

test("a take opens in the Desk as notation, with its score text and sections", async ({ page, request }) => {
  const take = await takeWithScore(request);
  await openFromTake(page, take.id);
  await expect(page.getByTestId("score-view").locator("svg")).toBeVisible();
  await expect(page.getByTestId("score-notation-failed")).toHaveCount(0);
  await page.getByText("Score text").click();
  await expect(page.getByTestId("plan-abc")).toContainText("K:");
  await expect(page.getByTestId("bar-editor")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  // the shell's player bar is not this page's
  expect((await smallTargets(page)).filter((s) => !/Previous|Play|Next|Seek|Now playing/.test(s.what))).toEqual([]);
  // the same take opened again is the same plan
  const url = page.url();
  await openFromTake(page, take.id);
  expect(page.url()).toBe(url);
});

test("a click on a note selects its bar and fills the editor; a tempo edit makes a new plan that names its parent", async ({ page, request }) => {
  const take = await takeWithScore(request);
  await openFromTake(page, take.id);
  const parentUrl = page.url();
  await page.getByTestId("score-view").locator(".abcjs-note").first().click({ force: true });
  await expect(page.getByTestId("edit-from")).toHaveValue("1");
  await expect(page.getByTestId("score-view").locator(".mm-bar-selected").first()).toBeAttached();

  await page.getByTestId("edit-mode-tempo").click();
  await page.getByTestId("edit-bpm").fill("97");
  await page.getByTestId("edit-rationale").fill("a little slower");
  await page.getByTestId("edit-apply").click();
  await expect(page).not.toHaveURL(parentUrl);
  await expect(page).toHaveURL(/\/desk\/plans\/[^/]+$/);
  await expect(page.getByTestId("plan-rationale")).toContainText("a little slower");
  await expect(page.getByTestId("plan-parent")).toBeVisible();

  // the parent stays as it was and lists the edit
  await page.goto(parentUrl);
  await expect(page.getByTestId("plan-children").getByRole("link")).toHaveCount(1);
});

test("a bad edit is refused in words and makes no plan", async ({ page, request }) => {
  const take = await takeWithScore(request);
  await openFromTake(page, take.id);
  await page.getByTestId("edit-voice").selectOption({ index: 1 });
  await page.getByTestId("edit-from").fill("1");
  await page.getByTestId("edit-to").fill("1");
  await page.getByTestId("edit-abc").fill("C");
  const url = page.url();
  await page.getByTestId("edit-apply").click();
  await expect(page.getByTestId("edit-refusal")).toBeVisible();
  expect(page.url()).toBe(url);
  // a shape error is caught before any call
  await page.getByTestId("edit-from").fill("0");
  await page.getByTestId("edit-apply").click();
  await expect(page.getByRole("alert").filter({ hasText: "Enter a bar" }).first()).toBeVisible();
});

test("the Owner likes and clears a plan; /desk groups plans by take and filters by verdict", async ({ page, request }) => {
  const take = await takeWithScore(request);
  await openFromTake(page, take.id);
  const plan = page.url();
  await page.getByTestId("verdict-like").click();
  await expect(page.getByTestId("plan-verdict-state")).toHaveAttribute("data-verdict", "liked");

  await page.goto("/desk");
  await expect(page.getByRole("heading", { name: "Desk", level: 1 })).toBeVisible();
  const group = page.getByTestId("desk-group").filter({ hasText: take.title });
  await expect(group).toBeVisible();
  await expect(group.getByTestId("desk-verdict").first()).toHaveAttribute("data-verdict", "liked");
  await page.getByTestId("desk-filter-verdict").selectOption("rejected");
  await expect(page.getByTestId("desk-empty")).toBeVisible();
  await page.getByTestId("desk-filter-verdict").selectOption("liked");
  await group.getByTestId("desk-plan").first().getByRole("link").click();
  await expect(page).toHaveURL(plan);

  await page.getByTestId("verdict-like").click(); // pressing it again clears it
  await expect(page.getByTestId("plan-verdict-state")).toHaveAttribute("data-verdict", "");
});

test("the score check reads pasted ABC and points at a syntax error", async ({ page }) => {
  await page.goto("/desk");
  await page.getByText("Check a score").click();
  await page.getByTestId("desk-check-abc").fill(TOY);
  await page.getByTestId("desk-check-run").click();
  await expect(page.getByTestId("desk-check-report")).toHaveAttribute("data-ok", "true");
  await expect(page.getByTestId("desk-check-report")).toContainText("2 bars");
  await page.getByTestId("desk-check-abc").fill("X:1\nT:Empty\nK:C\n");
  await page.getByTestId("desk-check-run").click();
  await expect(page.getByTestId("desk-check-report")).toHaveAttribute("data-ok", "false");
  await expect(page.getByTestId("desk-check-report")).toContainText("no notes");
});

test("the Desk row is in More under Make for the Owner", async ({ page }) => {
  await page.goto("/more");
  await expect(page.getByTestId("nav-desk")).toBeVisible();
  await page.getByTestId("nav-desk").click();
  await expect(page).toHaveURL(/\/desk$/);
});

test("a plan that does not exist says so", async ({ page }) => {
  await page.goto("/desk/plans/does-not-exist");
  await expect(page.getByTestId("plan-error")).toContainText("does not exist");
});

test("the API agrees: check_abc, the edit endpoint and the verdict endpoint answer an Owner", async ({ request }) => {
  const take = await takeWithScore(request);
  const open = await request.fetch(`/api/tracks/${take.id}/plan`, { method: "POST", headers: { "Idempotency-Key": idemKey() } });
  const plan = (await open.json()) as { id: string };
  expect((await request.post("/api/abc/check", { data: { abc: TOY } })).ok()).toBe(true);
  const v = await request.put(`/api/plans/${plan.id}/verdict`, { data: { verdict: "rejected" } });
  expect(v.ok()).toBe(true);
  const e = await request.fetch(`/api/plans/${plan.id}/edits`, { method: "POST", headers: { "Idempotency-Key": idemKey() }, data: { ops: [{ op: "set_tempo", bpm: 88 }] } });
  expect([200, 201]).toContain(e.status());
});
