// matrix: chrome-only because the refusal sentence is the same text in every layout; the sheet and 44 px rules are t1-transforms's
// The daily CPU allowance of the human transform pool is spent: the server answers 429 TRANSFORM_QUOTA_EXCEEDED and the panel
// explains it (the generic client drops the body of a 429, so the page rebuilds the sentence from the estimate). This spec file
// runs against a pool of 0 s (SPEC_ENV in server.ts); no transform of it ever renders.
import { expect, test } from "./fixtures";
import { idemKey } from "./helpers";
import { masterBody, t1Tracks, type T1Tracks } from "./fixtures/t1";
import { capabilities, listTransforms } from "./t1Api";

test.describe.configure({ mode: "serial" });

let t: T1Tracks;
test.beforeEach(async ({ request }) => {
  t = await t1Tracks(request);
});

test("the pool is empty: capabilities and the estimate answer, create is refused 429 with the quota in the body", async ({ request }) => {
  const caps = await capabilities(request);
  expect(caps.enabled).toBe(true);
  expect(caps.quota.cap_ms).toBe(0);

  const est = await request.post("/api/transforms/estimate", { data: masterBody(t.source.id) });
  expect(est.status()).toBe(200);

  const r = await request.post("/api/transforms", { data: masterBody(t.source.id), headers: { "Idempotency-Key": idemKey() } });
  expect(r.status()).toBe(429);
  const body = (await r.json()) as { code: string; details: { cap_ms: number; used_ms: number; reserved_ms: number; resets_at: string } };
  expect(body.code).toBe("TRANSFORM_QUOTA_EXCEEDED");
  expect(body.details.cap_ms).toBe(0);
  expect(body.details.used_ms + body.details.reserved_ms).toBe(0);
  expect(await listTransforms(request, `&track_id=${t.source.id}`)).toEqual([]);
});

test("the panel says the allowance is used up, names the figures, and nothing is queued", async ({ page, request }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));

  await page.goto(`/tracks/${t.source.id}`);
  const first = page.locator('[data-testid="transform-open-sheet"], [data-testid="transform-kinds"]').first();
  await first.waitFor({ state: "visible" });
  if ((await page.getByTestId("transform-open-sheet").count()) > 0) await page.getByTestId("transform-open-sheet").click();
  await page.getByTestId("transform-kind-master").click();
  await expect(page.getByTestId("transform-estimate-line")).toContainText("of CPU"); // the estimate is free and still answers

  await page.getByTestId("transform-render").click();
  const refused = page.getByTestId("transform-refused");
  await expect(refused).toBeVisible();
  await expect(refused).toContainText("Today's CPU allowance is used up");
  await expect(refused).toContainText("0 ms of 0 ms used");
  await expect(refused).not.toContainText("Too many requests");
  expect(await listTransforms(request, `&track_id=${t.source.id}`)).toEqual([]);

  await page.goto("/transforms");
  await expect(page.getByTestId("transforms-quota")).toContainText("Today's CPU");
  expect(errors).toEqual([]);
});
