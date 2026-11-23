// matrix: @mobile because the score page is a phone-width form (touch targets, no horizontal overflow)
import { expect, test } from "./fixtures";
import { smallTargets } from "./helpers";
import { capabilities, listTransforms, waitFor } from "./t1Api";

// The page offers Render only when the server lists score_render (a configured soundfont plus a working fluidsynth). Until
// the image carries fluidsynth (DESK-T3) the e2e server has neither, so the page must say so plainly and the API must agree
// (503 UNAVAILABLE naming the component). When they are there, the spec runs the real thing: paste, estimate, Render, done.
const ABC = "X:1\nT:E2E tune\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\nCDEF|G4|";

interface SoundfontCaps {
  soundfonts?: { name: string }[];
  tools?: { fluidsynth?: { available: boolean } };
}

test("the score page: Render when the synthesizer is there, a plain notice when it is not", async ({ page, request }) => {
  const caps = await capabilities(request);
  const full = caps as typeof caps & SoundfontCaps;
  const available = caps.kinds.some((k) => k.kind === "score_render") && (full.soundfonts?.length ?? 0) > 0 && full.tools?.fluidsynth?.available === true;

  await page.goto("/transforms/score");
  await expect(page.getByRole("heading", { name: "Render score" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  if (!available) {
    await expect(page.getByTestId("score-unavailable")).toContainText(/not available on this server yet/);
    await expect(page.getByTestId("score-render")).toHaveCount(0);
    // the server agrees: the estimate is refused with the component named, before the body is read
    const r = await request.fetch("/api/transforms/estimate", { method: "POST", data: { kind: "score_render", score_render: { score: { abc: ABC } } } });
    expect(r.status()).toBe(503);
    const problem = (await r.json()) as { code: string; details?: { component?: string } };
    expect(problem.code).toBe("UNAVAILABLE");
    expect(caps.enabled ? ["fluidsynth", "soundfont"] : ["transform"]).toContain(problem.details?.component);
    return;
  }

  await expect(page.getByTestId("score-form")).toBeVisible();
  // the global player bar (shown once a take has played) is the documented exception, as in desk-w1/desk-w2
  expect((await smallTargets(page)).filter((s) => !/Previous|Play|Next|Seek|Now playing/.test(s.what))).toEqual([]);
  await expect(page.getByTestId("score-render")).toBeDisabled();
  const before = new Set((await listTransforms(request, "&kind=score_render")).map((t) => t.id));
  await page.getByTestId("score-abc").fill(ABC);
  await expect(page.getByTestId("transform-estimate-line")).toContainText("$0.00");
  await expect(page.getByTestId("score-render")).toBeEnabled();
  await page.getByTestId("score-render").click();
  await expect(page.getByTestId("score-status")).toBeVisible();
  const fresh = async () => (await listTransforms(request, "&kind=score_render")).filter((t) => !before.has(t.id));
  await expect.poll(async () => (await fresh()).length, { timeout: 20_000 }).toBeGreaterThan(0);
  const row = (await fresh())[0]!;
  const done = await waitFor(request, row.id);
  expect(done.state).toBe("done");
});
