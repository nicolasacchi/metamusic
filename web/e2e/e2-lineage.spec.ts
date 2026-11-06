// matrix: chrome-only because the lineage badges and edges are plain DOM already covered on the phones by m3-lineage
import { expect, test } from "./fixtures";
import { apiJSON, trackByTitle } from "./helpers";
import { acePort, aceReference, aceBase, ensureAdopted, watchE2 } from "./e2Api";

test.describe.configure({ mode: "serial", timeout: 150_000 });

test.beforeEach(async ({ request, server }) => {
  await ensureAdopted(server, request);
});

interface Node {
  id: string;
  engine?: string | null;
  cross_engine?: boolean;
  parent_id: string | null;
  reference?: { track_id: string; from_s: number; to_s: number } | null;
}

test("a port shows the engine badge and the cross-engine badge; the YuE2 parent shows neither", async ({ page, request }) => {
  const watch = watchE2(page);
  const parent = await trackByTitle(request, "Syn song 03");
  const child = await acePort(request, parent.id);

  const lineage = await apiJSON<{ nodes: Node[] }>(request, "GET", `/api/tracks/${child}/lineage`);
  expect(lineage.nodes.find((n) => n.id === child)).toMatchObject({ engine: "acestep15", cross_engine: true, parent_id: parent.id });

  await page.goto(`/lineage/${child}`);
  const node = (id: string) => page.locator(`[data-testid="lineage-node"][data-id="${id}"]`);
  await expect(node(child)).toBeVisible();
  await expect(node(child).getByTestId("lineage-badge-engine")).toContainText("ACE-Step");
  await expect(node(child).getByTestId("lineage-badge-cross")).toHaveText("cross engine");
  await expect(node(child).getByTestId("lineage-operator")).toContainText(/engine[ _]port/i);
  await expect(node(parent.id).getByTestId("lineage-badge-engine")).toHaveCount(0);
  await expect(node(parent.id).getByTestId("lineage-badge-cross")).toHaveCount(0);
  // the take page of the child says the same
  await page.goto(`/tracks/${child}`);
  await expect(page.getByTestId("prov-operator")).toContainText(/engine[ _]port/i);
  expect(watch.problems()).toEqual([]);
});

test("a reference render shows the dashed reference edge to the take it followed", async ({ page, request }) => {
  const watch = watchE2(page);
  const source = await aceBase(request, 30);
  const child = await aceReference(request, source, 4, 14);

  const lineage = await apiJSON<{ nodes: Node[] }>(request, "GET", `/api/tracks/${child}/lineage`);
  expect(lineage.nodes.find((n) => n.id === child)?.reference).toMatchObject({ track_id: source, from_s: 4, to_s: 14 });

  await page.goto(`/lineage/${child}`);
  const edge = page.locator(`[data-testid="lineage-node"][data-id="${child}"]`).getByTestId("lineage-reference");
  await expect(edge).toBeVisible();
  await expect(edge).toContainText("reference:");
  await expect(edge).toContainText("4–14 s");
  await expect(edge.getByRole("link")).toHaveAttribute("href", `/tracks/${source}`);
  await expect(page.locator(`[data-testid="lineage-node"][data-id="${child}"]`).getByTestId("lineage-badge-engine")).toContainText("ACE-Step");
  expect(watch.problems()).toEqual([]);
});
