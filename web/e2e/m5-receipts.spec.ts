// matrix: chrome-only because Receipts is a read-only list over get_audit and list_batches; its phone layout is asserted in m5-layout
// M5 INT-W: the Receipts page. Activity grouped by who did it (a new group after 30 quiet minutes) with the money their batches
// settled, read from the audit log and the batch list through the SPA. Web-only: no operation of its own.
import { expect, test } from "./fixtures";
import { apiJSON, openNav } from "./helpers";
import { formatMicroUSD } from "../src/lib/money";
import { composeBase, spotAt, watchM5 } from "./m5Api";

test("Receipts groups the audit log by principal and shows what the human's batches settled", async ({ page, request, playwright, baseURL, agentToken }) => {
  const watch = watchM5(page);
  // the human spends (a composed take = one confirmed batch) and an agent writes: two principals in the log
  const base = await composeBase(request, "m5-receipts");
  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${agentToken}` } });
  try {
    await apiJSON(agent, "POST", `/api/tracks/${base.id}/comments`, { body: "m5 receipts: an agent note", kind: "note", at_second: spotAt(base.duration_s, 0.3) }, true);
  } finally {
    await agent.dispose();
  }
  const batches = (await apiJSON<{ items: { cost: { settled_micro_usd: number }; created_by: { kind: string } }[] }>(request, "GET", "/api/batches?limit=100")).items;
  const humanSettled = batches.filter((b) => b.created_by.kind === "human").reduce((n, b) => n + b.cost.settled_micro_usd, 0);
  expect(humanSettled).toBeGreaterThan(0);

  await page.goto("/more");
  await openNav(page, "Receipts");
  await expect(page).toHaveURL(/\/receipts$/);
  await expect(page.getByRole("heading", { name: "Receipts" })).toBeVisible();
  const table = page.getByTestId("receipts-table");
  await expect(table).toHaveAttribute("aria-busy", "false");
  await expect(page.getByTestId("receipts-unavailable")).toHaveCount(0);
  await expect(page.getByTestId("receipts-money-missing")).toHaveCount(0);
  await expect(page.getByTestId("receipts-empty")).toHaveCount(0);

  const groups = page.getByTestId("receipt-group");
  const human = groups.filter({ hasText: /^\s*human/i });
  const agentGroup = groups.filter({ hasText: /^\s*agent/i });
  await expect(human.first()).toBeVisible();
  await expect(agentGroup.first()).toBeVisible();
  await expect(human.first()).toContainText(/\d+ calls?/);
  await expect(human.first()).toContainText(/\d+ created/);

  // the money: the human groups together show exactly what the human's batches settled (integer micro-USD, formatted once)
  const spends = await human.getByTestId("receipt-spend").allTextContents();
  expect(spends.length).toBeGreaterThanOrEqual(1);
  for (const s of spends) expect(s).toMatch(/^\$\d/);
  if (spends.length === 1) expect(spends[0]).toBe(formatMicroUSD(humanSettled));
  await expect(agentGroup.first().getByTestId("receipt-spend")).toHaveText(formatMicroUSD(0));
  expect(watch.problems()).toEqual([]);
});
