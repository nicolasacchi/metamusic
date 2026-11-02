// matrix: @mobile because the Proposals tab (mapping checkboxes, Accept selected, the impact sentence) is decided from the phone
import { expect as baseExpect, test } from "./fixtures";
import { apiJSON } from "./helpers";
import { composeSong, letters, waitSongs } from "./l1Api";
import { agentContext } from "./l2Api";
import { getProposal, proposeTaxonomy } from "./l3Api";

// Two Songs each carry one unmapped style term of their own. An agent proposes mapping both; the human accepts only the
// first, then rejects a second proposal. Each test makes its own terms so no other proposal on this server matters.
const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 240_000 });

interface Search {
  total: number;
  songs: { id: string }[];
}
const inFacet = (request: Parameters<typeof apiJSON>[0], facet: string, value: string) => apiJSON<Search>(request, "GET", `/api/sounds/search?sound=${encodeURIComponent(`${facet}:${value}`)}&limit=10`);

async function songWithTerm(request: Parameters<typeof composeSong>[0], term: string) {
  const s = await composeSong(request, "l3p", 1, `english, synth pop, 100 bpm, ${term}`);
  await waitSongs(request, `track_id=${s.root}`);
  return s;
}

test("an agent proposes, the human accepts a subset: the Song appears under the facet, the other mapping stays unapplied", async ({ page, request, playwright, baseURL, agentToken }) => {
  const [t1, t2] = [letters("zp"), letters("zp")];
  const [v1, v2] = [letters("mood"), letters("mood")];
  const s1 = await songWithTerm(request, t1);
  const s2 = await songWithTerm(request, t2);
  const agent = await agentContext(playwright, baseURL, agentToken);
  const p = await proposeTaxonomy(agent, [
    { term: t1, facet: "mood", value: v1 },
    { term: t2, facet: "mood", value: v2 },
  ]);
  await agent.dispose();

  await page.goto("/sounds?tab=proposals");
  const card = page.getByTestId("proposal-card").filter({ hasText: t1 });
  await expect(card).toHaveCount(1);
  await expect(card.getByTestId("proposal-state")).toHaveText("open");
  await expect(card.getByTestId("proposal-rationale")).toHaveText("e2e: l3 proposal");
  await expect(card.getByTestId("proposal-impact")).not.toHaveText("");
  await expect(card.getByTestId("proposal-mapping")).toHaveCount(2);
  await card.getByTestId("proposal-refresh").click();
  await expect(card.getByTestId("proposal-impact")).toBeVisible();

  // untick the second mapping, accept the rest
  await card.getByTestId("proposal-check").nth(1).uncheck();
  await card.getByTestId("proposal-accept-selected").click();
  await expect(page.getByTestId("proposal-outcome")).toContainText(/accepted|applied/i);
  await expect(page.getByTestId("proposal-card").filter({ hasText: t1 })).toHaveCount(0); // closed: off the open list

  await expect.poll(async () => (await getProposal(request, p.id))?.state).toBe("accepted");
  expect((await getProposal(request, p.id))?.accepted).toEqual([0]);
  await expect.poll(async () => (await inFacet(request, "mood", v1)).songs.map((s) => s.id)).toEqual([s1.songId]);
  expect((await inFacet(request, "mood", v2)).total).toBe(0);
  expect(s2.songId).not.toBe(s1.songId);

  await page.getByTestId("proposals-history").click();
  const done = page.getByTestId("proposal-card").filter({ hasText: t1 });
  await expect(done).toHaveCount(1);
  await expect(done.getByTestId("proposal-state")).toHaveText("accepted");
  await expect(done.getByTestId("proposal-accept")).toHaveCount(0);
});

test("Reject closes a proposal without mapping anything", async ({ page, request, playwright, baseURL, agentToken }) => {
  const t = letters("zr");
  const v = letters("mood");
  await songWithTerm(request, t);
  const agent = await agentContext(playwright, baseURL, agentToken);
  const p = await proposeTaxonomy(agent, [{ term: t, facet: "mood", value: v }]);
  await agent.dispose();

  await page.goto("/sounds?tab=proposals");
  const card = page.getByTestId("proposal-card").filter({ hasText: t });
  await expect(card).toHaveCount(1);
  await card.getByTestId("proposal-note-input").fill("e2e: not this one");
  await card.getByTestId("proposal-reject").click();
  await expect(page.getByTestId("proposal-card").filter({ hasText: t })).toHaveCount(0);
  await expect.poll(async () => (await getProposal(request, p.id))?.state).toBe("rejected");
  expect((await inFacet(request, "mood", v)).total).toBe(0);
});

test("agents read proposals but cannot decide them", async ({ request, playwright, baseURL, agentToken }) => {
  const t = letters("zq");
  await songWithTerm(request, t);
  const agent = await agentContext(playwright, baseURL, agentToken);
  const p = await proposeTaxonomy(agent, [{ term: t, facet: "mood", value: letters("mood") }]);
  const list = await agent.get("/api/taxonomy-proposals?state=open&limit=50");
  expect(list.ok()).toBe(true);
  expect(((await list.json()) as { items: { id: string }[] }).items.map((x) => x.id)).toContain(p.id);
  const decide = await agent.post(`/api/taxonomy-proposals/${p.id}/decision`, { data: { decision: "accept" } });
  expect(decide.status()).toBe(403);
  expect(((await decide.json()) as { code: string }).code).toBe("HUMAN_ONLY");
  await agent.dispose();
  expect((await getProposal(request, p.id))?.state).toBe("open");
  // a human may not propose either: propose_taxonomy is the agent's face
  const human = await request.post("/api/taxonomy-proposals", { data: { rationale: "x", mappings: [{ term: t, facet: "mood", value: "x", op: "add" }] }, headers: { "Idempotency-Key": `h-${t}` } });
  expect(human.status()).toBe(403);
});
