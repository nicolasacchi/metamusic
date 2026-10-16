// matrix: chrome-only because it is a settings form with no device-dependent behaviour
import { expect, test } from "./fixtures";
import { apiJSON } from "./helpers";
import { recipe } from "./m3Api";

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

// The minimum-render control (X4): with the shipped 180 s minimum an estimate forces the take to at least that length,
// quotes it so and says so; the e2e template runs with 30 s so the fake backend's short takes stay cheap.
test("a 180 s minimum forces and quotes the take, and the estimate says so", async ({ request }) => {
  const before = await apiJSON<Targets>(request, "GET", "/api/settings/songwriting");
  interface Est {
    items: { takes: { expected_audio_s: number }[] }[];
    warnings: { take_index: number; code: string }[];
  }
  const est = () => apiJSON<Est>(request, "POST", "/api/generations/estimate", { items: [{ mode: "compose_new", title: "e2e floor", recipe: recipe("e2e-floor", 424242) }] });
  try {
    await apiJSON(request, "PUT", "/human/api/settings/songwriting", { min_duration_s: 30 });
    expect((await est()).warnings.some((w) => w.code === "min_audio_forced")).toBe(true); // 70 percent of the estimate still applies
    await apiJSON(request, "PUT", "/human/api/settings/songwriting", { min_duration_s: 180 });
    const e = await est();
    expect(e.items[0]!.takes[0]!.expected_audio_s).toBeGreaterThanOrEqual(180);
    expect(e.warnings.some((w) => w.code === "min_audio_forced")).toBe(true);
  } finally {
    await apiJSON(request, "PUT", "/human/api/settings/songwriting", before);
  }
});
