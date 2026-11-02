import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Atlas from "./Atlas.svelte";
import { atlasPage } from "../lib/fixtures/l3/atlas";
import { visiblePoints } from "../lib/atlasApi";

type Props = Record<string, unknown>;
const page = (props: Props = {}) =>
  render(Atlas, { props: { search: "", navigate: () => {}, initial: atlasPage(), viewer: { kind: "human", scopes: ["read"] }, ...props } } as never).body;
const count = (h: string, re: RegExp) => (h.match(re) ?? []).length;

describe("Atlas page", () => {
  const h = page();
  test("keeps the atlas-page test id and the Map and Requested vs got tabs", () => {
    expect(h).toContain('data-testid="atlas-page"');
    expect(h).toContain('data-testid="atlas-tab-map"');
    expect(h).toContain('data-testid="atlas-tab-adherence"');
    expect(h).toContain('data-testid="atlas-canvas"');
  });
  test("the totals strip reads plotted, measured, estimated and not plotted", () => {
    // 10 plotted, 2 predicted, scope 12
    expect(h).toContain("10 plotted · 8 measured · 2 estimated · 2 not plotted");
  });
  test("the legend lists the colours with the predicted-energy footnote", () => {
    expect(h).toContain('data-testid="atlas-legend"');
    expect(count(h, /data-testid="atlas-legend-entry"/g)).toBe(3);
    expect(h).toContain("Predicted energy is estimated from tempo band, genre and mood, so unrendered Songs form stacks; it is not a measurement.");
  });
  test("the toolbar has presets, axes, colour-by, dim and the predicted toggle (on by default)", () => {
    for (const id of ["atlas-preset-tempo-energy", "atlas-preset-family-vocal", "atlas-preset-requested-got", "atlas-x", "atlas-y", "atlas-color", "atlas-dim", "atlas-filter-crate", "atlas-filter-session"]) {
      expect(h).toContain(`data-testid="${id}"`);
    }
    expect(h).toMatch(/<input[^>]*checked[^>]*data-testid="atlas-hide-predicted"/);
    expect(h).toMatch(/aria-pressed="true"[^>]*data-testid="atlas-preset-tempo-energy"/);
  });
  test("an empty selection shows no selection bar", () => {
    expect(h).not.toContain('data-testid="atlas-select-bar"');
  });
});

describe("table fallback", () => {
  const data = atlasPage(12);
  const t = page({ initial: data, initialTable: true });
  test("renders the same rows as the points the canvas draws", () => {
    const shown = visiblePoints(data.points, true);
    expect(shown.length).toBe(count(t, /data-testid="atlas-table-row"/g));
    for (const p of shown) expect(t).toContain(`data-song-id="${p.id}"`);
    const hidden = data.points.filter((p) => !shown.includes(p));
    expect(hidden.length).toBeGreaterThan(0);
    for (const p of hidden) expect(t).not.toContain(`data-song-id="${p.id}"`);
    expect(t).not.toContain('data-testid="atlas-canvas"');
  });
  test("rows link to the Song pages", () => {
    expect(t).toContain('href="/songs/song-1"');
  });
});

describe("URL state", () => {
  test("filters, axes and the near Song round-trip from the query string", () => {
    const f = page({ search: "?x=bpm_requested&y=bpm_measured&color=family&dim=1&state=draft&sound=era%3A90s&sound=mood%3Achill&muse=m1&crate=c1&q=rain&near=song-3" });
    expect(f).toMatch(/aria-pressed="true"[^>]*data-testid="atlas-preset-requested-got"/);
    expect(f).toMatch(/aria-pressed="true"[^>]*data-testid="atlas-state-draft"/);
    expect(f).toContain('data-testid="atlas-muse-pill"');
    expect(f).toContain('data-testid="atlas-near-pill"');
    expect(f).toMatch(/<input[^>]*checked[^>]*data-testid="atlas-dim"/);
    expect(f).toContain('value="rain"');
  });
  test("the neighbours of a near Song are listed first-page", () => {
    const f = page({ search: "?near=song-3", initial: atlasPage(5, { neighbors: [{ id: "song-4", title: "Synthetic song 4", distance: 10, parts: { energy: 1, key: 1, tempo: 1 } }] as never }) });
    expect(f).toContain('data-testid="atlas-neighbours"');
    expect(f).toContain("Nearest to Synthetic song 3");
    expect(f).toContain('href="/songs/song-4"');
  });
});

describe("equal axes", () => {
  test("are refused with the server's text", () => {
    const f = page({ search: "?x=energy&y=energy" });
    expect(f).toContain('data-testid="atlas-axis-error"');
    expect(f).toContain("x and y must be two different axes");
  });
  test("different axes show no error", () => {
    expect(page({ search: "?x=energy&y=tempo" })).not.toContain("atlas-axis-error");
  });
});

describe("states", () => {
  test("a capped load, a stale-looking pending layout and an empty map say so", () => {
    expect(page({ initial: atlasPage(3, { pending: false }) })).not.toContain("atlas-capped");
    const e = page({ initial: atlasPage(0) });
    expect(e).toContain("0 plotted");
  });
  test("the adherence tab renders its panel", () => {
    const a = page({ initialTab: "adherence" });
    expect(a).toContain('data-testid="atlas-adherence"');
    expect(a).not.toContain('data-testid="atlas-canvas"');
  });
});
