import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import TransformScore from "../routes/TransformScore.svelte";
import type { components } from "../api/schema";
import { APP_ROUTES } from "./router";
import { NAV_LINKS, ROUTE_OPS, routeAllowed, tabFor, titleFor } from "./nav";
import { buildScoreRequest, defaultScoreForm, scoreAvailability, scoreRefusalText, scoreUnavailableRefusal, type ScoreForm } from "./transformScore";

type Caps = components["schemas"]["TransformCapabilities"];
const kindRow = { kind: "score_render", summary: "s", inputs: 1, params: [], blind_spots: [], example: {} } as unknown as Caps["kinds"][number];
const caps = (o: { kinds?: Caps["kinds"]; fonts?: string[]; fluid?: boolean; enabled?: boolean } = {}): Caps =>
  ({
    enabled: o.enabled ?? true,
    kinds: o.kinds ?? [kindRow],
    soundfonts: (o.fonts ?? ["alpha", "beta"]).map((name) => ({ name, bytes: 1, sha256_12: "abc" })),
    tools: { ffmpeg: true, flac: true, fluidsynth: { available: o.fluid ?? true, version: "2.3" } },
  }) as unknown as Caps;
const form = (o: Partial<ScoreForm> = {}): ScoreForm => ({ ...defaultScoreForm(), ...o });
const ABC = "X:1\nT:t\nM:4/4\nL:1/4\nK:C\nCDEF|";

describe("availability", () => {
  test("available when the kind is listed, a font exists and fluidsynth runs", () => {
    expect(scoreAvailability(caps())).toEqual({ available: true, soundfonts: ["alpha", "beta"] });
  });
  test("each missing piece is a plain reason, never a click-time failure", () => {
    const a = scoreAvailability(caps({ kinds: [], fluid: false, fonts: [] }));
    expect(a.available).toBe(false);
    expect(a.available === false && a.reason).toContain("fluidsynth");
    const f = scoreAvailability(caps({ kinds: [], fluid: true, fonts: [] }));
    expect(f.available === false && f.reason).toContain("soundfont");
    const k = scoreAvailability(caps({ kinds: [] }));
    expect(k.available).toBe(false);
    const off = scoreAvailability(caps({ enabled: false }));
    expect(off.available === false && off.reason).toContain("not available");
    expect(scoreAvailability(null).available).toBe(false);
  });
});

describe("request body", () => {
  test("pasted ABC with defaults blank goes out as nulls", () => {
    const b = buildScoreRequest(form({ abc: ABC }));
    expect(b.errors).toEqual({});
    expect(b.body as unknown).toEqual({
      kind: "score_render",
      title: null,
      score_render: { score: { abc: ABC }, dialect: null, soundfont: null, bpm: null, tail_s: null, gain: null, chords: null },
    });
  });
  test("a take with every field set", () => {
    const b = buildScoreRequest(form({ source: "take", trackId: " t-1 ", soundfont: "beta", dialect: "yue2", chords: "block", bpm: "96", tail: "2.5", gain: "0.8", title: " Take " }));
    expect(b.errors).toEqual({});
    expect(b.body as unknown).toEqual({
      kind: "score_render",
      title: "Take",
      score_render: { score: { from_track_id: "t-1" }, dialect: "yue2", soundfont: "beta", bpm: 96, tail_s: 2.5, gain: 0.8, chords: "block" },
    });
  });
  test("errors per field and no body", () => {
    expect(buildScoreRequest(form()).errors.abc).toBeDefined();
    expect(buildScoreRequest(form({ source: "take" })).errors.trackId).toBeDefined();
    const e = buildScoreRequest(form({ abc: ABC, bpm: "5", tail: "x", gain: "-1" }));
    expect(e.body).toBeNull();
    expect(Object.keys(e.errors).sort()).toEqual(["bpm", "gain", "tail"]);
    expect(buildScoreRequest(form({ abc: "x".repeat(262144 + 1) })).errors.abc).toContain("too long");
  });
});

describe("refusals", () => {
  const r = (code: string, details: Record<string, unknown> = {}, status = 422) => ({ status, code, message: "", details });
  test("an unavailable component is recognised and worded plainly", () => {
    const u = r("UNAVAILABLE", { component: "fluidsynth" }, 503);
    expect(scoreUnavailableRefusal(u)).toBe(true);
    expect(scoreRefusalText(u)).toContain("fluidsynth");
    expect(scoreUnavailableRefusal(r("UNAVAILABLE", { component: "soundfont" }, 503))).toBe(true);
    expect(scoreUnavailableRefusal(r("UNAVAILABLE", { component: "transform" }, 503))).toBe(false);
    expect(scoreUnavailableRefusal(r("SCORE_INVALID"))).toBe(false);
  });
  test("a score refusal names the rule and the position", () => {
    expect(scoreRefusalText(r("SCORE_INVALID", { rule: "no_score", line: 3, col: 2 }))).toContain("line 3");
    expect(scoreRefusalText(r("SCORE_INVALID", { rule: "empty" }))).toContain("empty");
    expect(scoreRefusalText(r("SOUNDFONT_UNKNOWN", { soundfont: "zz" }))).toContain("soundfont");
    expect(scoreRefusalText(r("TRANSFORM_TOO_LARGE", { limit: "output_seconds" }))).toContain("too large");
  });
});

describe("navigation", () => {
  test("/transforms/score is a More route in Make under Transforms", () => {
    expect(APP_ROUTES.find((x) => x.name === "transform-score")?.pattern).toBe("/transforms/score");
    expect(tabFor("transform-score")).toBe("more");
    expect(titleFor("transform-score")).toBe("Render score");
    expect(ROUTE_OPS["transform-score"]).toBe("create_transform");
    expect(routeAllowed("transform-score", () => false)).toBe(false);
    const i = NAV_LINKS.findIndex((l) => l.path === "/transforms");
    expect(NAV_LINKS[i + 1]).toMatchObject({ label: "Render score", path: "/transforms/score", tab: "more", group: "Make" });
  });
});

describe("the page", () => {
  const html = (props: Record<string, unknown>) => render(TransformScore as never, { props } as never).body;
  test("without fluidsynth it says so and offers no form or Render button", () => {
    const h = html({ initialCaps: caps({ kinds: [], fluid: false }) });
    expect(h).toContain('data-testid="score-unavailable"');
    expect(h).toContain("fluidsynth");
    expect(h).not.toContain('data-testid="score-render"');
  });
  test("with the kind available the form lists the soundfonts and Render starts disabled", () => {
    const h = html({ initialCaps: caps() });
    expect(h).toContain('data-testid="score-form"');
    expect(h).toContain("<option value=\"beta\">beta</option>");
    expect(h).toMatch(/data-testid="score-render"/);
    expect(h).toMatch(/disabled[^>]*data-testid="score-render"|data-testid="score-render"[^>]*disabled/);
  });
  test("?track_id= opens on the take source", () => {
    expect(html({ initialCaps: caps(), trackId: "t-9" })).toContain('value="t-9"');
  });
});
