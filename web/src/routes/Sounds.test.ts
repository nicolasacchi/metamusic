import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Sounds from "./Sounds.svelte";

const initial = {
  taxonomy_version: 1,
  selected: { genre_family: ["reggae"] },
  facets: {
    genre_family: [
      { value: "pop", count: 1 },
      { value: "reggae", count: 3 },
    ],
    tempo_band: [{ value: "slow", count: 2, mismatches: 1 }],
  },
  songs: [],
  total: 0,
  next_cursor: null,
};
const html = (search: string) => render(Sounds, { props: { search, navigate: () => {}, initial, viewer: { kind: "human", scopes: ["read"] } } } as never).body;

describe("Sounds page", () => {
  test("renders facets in fixed order, selected pills and pressed values from the URL", () => {
    const h = html("?sound=genre_family:reggae&tempo_mismatch=true");
    expect(h.indexOf("facet-genre_family")).toBeLessThan(h.indexOf("facet-tempo_band"));
    expect(h).toContain('data-testid="selected-pill"');
    expect(h).toContain("Genre: reggae");
    expect(h).toMatch(/data-testid="tempo-mismatch-toggle"[^>]*checked|checked[^>]*data-testid="tempo-mismatch-toggle"/);
    expect(h).toContain("⚠ 1");
    // reggae (3) before pop (1)
    expect(h.indexOf('data-value="reggae"')).toBeLessThan(h.indexOf('data-value="pop"'));
  });
  test("no pills without selection", () => {
    expect(html("")).not.toContain('data-testid="selected-pill"');
  });
});
