// matrix: chrome-only because it is a settings form with no device-dependent behaviour
import { expect, test } from "@playwright/test";
import { apiJSON } from "./helpers";

// The operator sets the songwriting duration targets on Settings; the agent read (get_songwriting_settings) and the
// lint warning see the change. The defaults are put back in `finally`.

test.describe.configure({ mode: "serial" });

interface Targets {
  min_duration_s: number;
  target_min_duration_s: number;
  target_max_duration_s: number;
}

test("the operator changes the minimum and the target band on Settings", async ({ page, request }) => {
  const before = await apiJSON<Targets>(request, "GET", "/api/settings/songwriting");
  try {
    await page.goto("/settings");
    const min = page.getByTestId("sw-min");
    await expect(min).toHaveValue(String(before.min_duration_s));
    await min.fill("200");
    await page.getByTestId("sw-target-max").fill("330");
    await page.getByTestId("sw-save").click();
    await expect(page.getByTestId("sw-saved")).toBeVisible();

    const after = await apiJSON<Targets>(request, "GET", "/api/settings/songwriting");
    expect(after).toEqual({ ...before, min_duration_s: 200, target_max_duration_s: 330 });

    // an out-of-order band is refused and shown, and the stored values stay
    await page.getByTestId("sw-target-min").fill("400");
    await page.getByTestId("sw-save").click();
    await expect(page.getByTestId("songwriting-error")).toBeVisible();
    expect(await apiJSON<Targets>(request, "GET", "/api/settings/songwriting")).toEqual(after);

    // the lint warning carries the new values
    const lint = await apiJSON<{ items: { warnings: { code: string; details?: Record<string, number> }[] }[] }>(request, "POST", "/api/recipes/lint", {
      items: [{ request: { id: "e2e-sw", style: "english, 100 bpm", lyrics: "[Verse]\noro kala\nvenu tilo\nmira dosa\nlano pera\n" } }],
    });
    const w = lint.items[0]?.warnings.find((x) => x.code === "duration_under_target");
    expect(w?.details?.min_s).toBe(200);
    expect(w?.details?.target_max_s).toBe(330);
  } finally {
    await apiJSON(request, "PUT", "/human/api/settings/songwriting", before);
  }
});
