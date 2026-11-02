// matrix: @mobile because What next is a section of the New landing page (tappable cards, 44 px targets, needs-you counts) read on the phone
import { expect as baseExpect, test } from "./fixtures";
import { apiJSON, uniq } from "./helpers";
import { assignSongs, createMuse, getMuse } from "./l1Api";
import { agentContext, createRecord, retireRecord, type RecordOut } from "./l2Api";
import { nextActions, proposeTaxonomy, songsAt, type TitledAt } from "./l3Api";

// The spec's own Muse (two rendered, unjudged Songs), a proposed Record and an open taxonomy proposal give What next its
// human-audience items. It never confirms spend: the estimate hand-off is checked on a stubbed list and stops at the page.
const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 240_000 });

const tag = uniq("l3n");
let songs: TitledAt[] = [];
let museId = "";
let record: RecordOut | undefined;

const section = (page: import("./fixtures").Page) => page.getByTestId("next-actions");

test("setup: a Muse with unjudged Songs, a proposed Record, an open taxonomy proposal", async ({ request, playwright, baseURL, agentToken }) => {
  songs = await songsAt(request, tag, [100, 120]);
  museId = (await createMuse(request, uniq("l3 next muse"))).id;
  await assignSongs(request, museId, songs.map((s) => s.songId));
  const r = await createRecord(request, uniq("l3 next record"), songs.map((s) => s.songId));
  record = await apiJSON<RecordOut>(request, "POST", `/api/records/${r.record.id}/proposal`, { revision: r.record.revision, rationale: "e2e: proposed for review" });
  expect(record.record.status).toBe("proposed");
  const agent = await agentContext(playwright, baseURL, agentToken);
  await proposeTaxonomy(agent, [{ term: `${tag.replace(/[^a-z]/g, "")}term`, facet: "mood", value: "calm" }]);
  await agent.dispose();
  const items = await nextActions(request);
  expect(items.map((a) => a.code)).toEqual(expect.arrayContaining(["record_proposed_waiting", "muse_unjudged", "taxonomy_proposals_open"]));
});

test("New shows What next grouped by rule, with the human items marked needs you", async ({ page }) => {
  await page.goto("/");
  await expect(section(page)).toBeVisible();
  const cards = page.getByTestId("next-action-card");
  await expect(cards.first()).toBeVisible();
  await expect(page.getByTestId("next-actions-count")).toHaveText(String(await cards.count()));
  // one group per code: every card of a code sits in its group, and no code is split in two
  const codes = await section(page).locator(".group").evaluateAll((els) => els.map((e) => e.getAttribute("data-code") ?? ""));
  expect(new Set(codes).size).toBe(codes.length);
  const duels = section(page).locator('.group[data-code="song_duel"] [data-testid="next-action-card"]');
  expect(await duels.count()).toBeGreaterThanOrEqual(2);
  // the proposed Record waits for the operator
  const waiting = cards.and(page.locator('[data-code="record_proposed_waiting"]'));
  await expect(waiting).toHaveCount(1);
  await expect(waiting.getByTestId("next-action-needs")).toHaveText("needs you");
  await expect(cards.and(page.locator('[data-code="song_duel"]')).first().getByTestId("next-action-needs")).toHaveCount(0);
});

test("a Muse card opens the Muse page; a Record card opens the Record", async ({ page }) => {
  await page.goto("/");
  await section(page).locator('[data-code="muse_unjudged"] a').first().click();
  await expect(page).toHaveURL(new RegExp(`/muses/${museId}$`));
  await page.goto("/");
  await section(page).locator('[data-code="record_proposed_waiting"] a').first().click();
  await expect(page).toHaveURL(new RegExp(`/records/${record!.record.id}$`));
  await expect(page.getByTestId("record-status")).toContainText(/proposed/i);
});

test("the taxonomy card opens the Proposals tab", async ({ page }) => {
  await page.goto("/");
  await section(page).locator('[data-code="taxonomy_proposals_open"] a').first().click();
  await expect(page).toHaveURL(/\/sounds\?tab=proposals/);
  await expect(page.getByTestId("taxonomy-proposals")).toBeVisible();
});

test("the needs-you section counts the proposed Record and the open proposal; the Record link opens the list filtered", async ({ page, request }) => {
  const proposed = (await apiJSON<{ total: number }>(request, "GET", "/api/records?status=proposed&limit=1")).total;
  expect(proposed).toBeGreaterThanOrEqual(1);
  await page.goto("/");
  await expect(page.getByTestId("need-records")).toContainText(`${proposed} proposed Record${proposed === 1 ? "" : "s"} to review`);
  await expect(page.getByTestId("need-taxonomy")).toBeVisible();
  await page.getByTestId("need-records").getByRole("link").click();
  await expect(page).toHaveURL(/\/records\?status=proposed/);
  await expect(page.getByTestId("records-list")).toBeVisible();
  await expect(page.getByRole("button", { name: "Proposed" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByRole("button", { name: "Draft" })).toHaveAttribute("aria-pressed", "false");
  await expect(page.getByText(record!.record.title)).toBeVisible();
});

test("an estimate hint hands a drafts intent to /human/generate and never calls the API", async ({ page }) => {
  let created = 0;
  page.on("request", (r) => {
    // the generate page asks for an estimate (free); creating a batch is the spend this spec must never reach
    if (r.method() === "POST" && /\/api\/batches/.test(r.url())) created++;
  });
  await page.route("**/api/next-actions*", async (route) => {
    await route.fulfill({
      json: {
        generated_at: new Date().toISOString(),
        total: 1,
        next_cursor: null,
        groups: [{ code: "muse_unrendered", more: 0 }],
        items: [{ code: "muse_unrendered", priority: 70, audience: "human", subject: { kind: "muse", id: museId }, text: "A stubbed item: render the drafts", evidence: {}, hint: { op: "estimate_generation", args: { items: [{ mode: "from_draft", draft_ids: ["d-e2e-1"] }] } } }],
      },
    });
  });
  await page.goto("/");
  await section(page).getByRole("link", { name: /stubbed item/ }).click();
  await expect(page).toHaveURL(/\/human\/generate/);
  expect(created).toBe(0);
});

test("a hidden Muse's item disappears", async ({ page, request }) => {
  await page.goto("/");
  await expect(section(page).getByTestId("next-action-card").and(page.locator('[data-code="muse_unjudged"]'))).toHaveCount(1);
  const m = await getMuse(request, museId);
  await apiJSON(request, "PATCH", `/api/muses/${museId}`, { revision: m.muse.revision, hidden: true });
  await page.reload();
  await expect(section(page).getByTestId("next-actions-count")).toBeVisible();
  await expect(section(page).getByTestId("next-action-card").and(page.locator('[data-code="muse_unjudged"]'))).toHaveCount(0);
});

test("the What next cards are at least 44 px tall on a phone", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 1280) >= 768, "phone layout");
  await page.goto("/");
  const cards = page.getByTestId("next-action-card");
  await expect(cards.first()).toBeVisible();
  for (const c of await cards.all()) expect((await c.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(43.5);
});

test.afterAll(async ({ request }) => {
  if (record) await retireRecord(request, record.record.id).catch(() => undefined);
});
