// matrix: chrome-only because the Listener shell is asserted once, in the listener project (desktop Chrome signed in as carol)
import { expect, test, type Page } from "./fixtures";
import { ownTracks } from "./helpers";
import { asOwner, ratingOf, setStatus } from "./u1Api";

// U1 C314.7: the Listener sees the whole library read-only and none of the Owner's money, approvals or secrets.
// Identity comes from the project's extraHTTPHeaders (carol); the Owner acts through a request context.

const MAKE_AND_SECURE = ["Board", "Backlog", "Jobs", "Transforms", "Desk", "Engines", "Generate", "Approvals", "Spend", "Tokens", "Connectors", "Audit", "Backups", "Receipts"];

async function batchCalls(page: Page, during: () => Promise<void>): Promise<string[]> {
  const seen: string[] = [];
  const on = (r: { url(): string }) => {
    const p = new URL(r.url()).pathname;
    if (p === "/api/batches" || p.startsWith("/api/batches/")) seen.push(p);
  };
  page.on("request", on);
  try {
    await during();
  } finally {
    page.off("request", on);
  }
  return seen;
}

test("More: no Make, Spend or approval rows, no approvals badge or polling; My devices is there", async ({ page }) => {
  const calls = await batchCalls(page, async () => {
    await page.goto("/more");
    await expect(page.getByTestId("identity")).toHaveText("carol");
    await expect(page.getByTestId("via")).toContainText("listen");
    await expect(page.getByRole("heading", { name: "Make" })).toHaveCount(0);
    for (const label of MAKE_AND_SECURE) await expect(page.getByRole("main").getByRole("link", { name: new RegExp(`^${label}\\b`) }), label).toHaveCount(0);
    await expect(page.getByTestId("approvals-nav")).toHaveCount(0);
    await expect(page.getByTestId("approvals-row-badge")).toHaveCount(0);
    await expect(page.getByTestId("approvals-badge")).toHaveCount(0);
    // after D1 a Listener pairs their own phone: the Devices row stays
    await expect(page.getByRole("main").getByRole("link", { name: /^Devices\b/ })).toBeVisible();
    await page.goto("/");
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
    await page.waitForTimeout(500);
  });
  expect(calls, "a Listener never polls the batch list").toEqual([]);
});

test("Settings: the Listener picks this device's playback quality and sees none of the Owner's sections", async ({ page }) => {
  await page.goto("/settings");
  await expect(page.getByRole("heading", { name: "No access" })).toHaveCount(0);
  await expect(page.getByTestId("device-settings")).toBeVisible();
  await expect(page.locator('input[name="quality"][value="lossless"]')).toBeVisible();
  for (const id of ["system-info", "device-profiles", "probe-test", "songwriting-targets"]) await expect(page.getByTestId(id), id).toHaveCount(0);
  await expect(page.getByRole("link", { name: /^(Backups|Audit log)$/ })).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("the Owner's pages render no access and /human/api refuses the Listener", async ({ page, request }) => {
  for (const path of ["/human/spend", "/human/tokens", "/human/generate", "/settings/audit", "/jobs"]) {
    await page.goto(path);
    await expect(page.getByRole("heading", { name: "No access" }), path).toBeVisible();
  }
  for (const [method, path] of [
    ["GET", "/api/tokens"],
    ["GET", "/api/spend"],
    ["PUT", "/human/api/spend/caps"],
    ["POST", "/human/api/batches/none/approve"],
    ["POST", "/human/api/batches/none/reject"],
  ] as const) {
    const r = await request.fetch(path, { method, ...(method === "GET" ? {} : { data: method === "PUT" ? { monthly_micro_usd: 1_000_000 } : {} }), headers: { "Idempotency-Key": `e2e-l-${Math.random().toString(36).slice(2)}` } });
    expect(r.status(), `${method} ${path}`).toBe(403);
  }
  // the one /human/api route a Listener may call is their own device list
  expect((await request.get("/human/api/devices")).status()).toBe(200);
});

test("a Listener rates their own take: the Owner's rating and the Owner view are separate, the Listener sees no average or others", async ({ page, request, playwright, baseURL }, info) => {
  const [t] = await ownTracks(request, info);
  const owner = await asOwner(playwright, baseURL);
  try {
    const before = (await ratingOf(owner, t.id)).mine?.stars ?? null;
    await page.goto(`/tracks/${t.id}`);
    await page.getByTestId("track-judge").getByRole("button", { name: "4 stars", exact: true }).click();
    await expect.poll(async () => (await ratingOf(request, t.id)).mine?.stars).toBe(4);
    const mine = await ratingOf(request, t.id);
    expect(mine.others ?? null).toBeNull();
    expect(mine.average ?? null).toBeNull();
    await expect(page.getByTestId("rating-panel")).toHaveCount(0);
    // the Owner's own rating did not move, and the Owner sees carol among the others
    const o = await ratingOf(owner, t.id);
    expect(o.mine?.stars ?? null).toBe(before);
    expect(o.others?.map((x) => [x.user, x.stars])).toContainEqual(["carol", 4]);
  } finally {
    await owner.dispose();
  }
});

test("a take the Owner discards is gone for the Listener", async ({ page, request, playwright, baseURL }, info) => {
  const [, t] = await ownTracks(request, info);
  const owner = await asOwner(playwright, baseURL);
  try {
    const listed = async () => ((await (await request.get("/api/tracks?limit=50")).json()) as { items: { id: string }[] }).items.map((x) => x.id);
    expect(await listed()).toContain(t.id);
    await setStatus(owner, t.id, "discarded");
    expect(await listed()).not.toContain(t.id);
    expect((await request.get(`/api/tracks/${t.id}`)).status()).toBe(404);
    await page.goto("/library");
    await expect(page.locator("[data-testid=track-table], [data-testid=track-list]")).toBeVisible();
    await expect(page.locator(`a[href="/tracks/${t.id}"]`)).toHaveCount(0);
  } finally {
    await setStatus(owner, t.id, "draft");
    await owner.dispose();
  }
});
