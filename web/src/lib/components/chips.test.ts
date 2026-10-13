import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import LibraryFilters from "./LibraryFilters.svelte";
import TrackTable from "./TrackTable.svelte";
import DuelCard from "./DuelCard.svelte";
import DuelReveal from "./DuelReveal.svelte";
import { defaultFilters } from "../libraryQuery";
import { summaryKnown, summaryLossy, summaryReference } from "../fixtures/m2c";
import { duelOpenBlindLossy, duelRevealedLossy } from "../fixtures/m2c";

const noop = () => {};
// ABPlayer cancels an animation frame on destroy; the SSR run has no window. A no-op is all it needs.
(globalThis as { cancelAnimationFrame?: (n: number) => void }).cancelAnimationFrame ??= () => {};
const html = (c: unknown, props: Record<string, unknown>) => render(c as never, { props } as never).body;

describe("LibraryFilters (M2c)", () => {
  test("renders the recipe and audio selects with the active values", () => {
    const h = html(LibraryFilters, { filters: { ...defaultFilters(), recipeKnown: false, lossy: true }, facets: null, onchange: noop });
    expect(h).toContain('name="recipe_known"');
    expect(h).toContain('name="lossy"');
    expect(h).toContain("Recipe");
    expect(h).toContain("unknown");
    expect(h).toContain("lossless");
    expect(h).toMatch(/<option value="false" selected[^>]*>/);
    expect(h).toMatch(/<option value="true" selected[^>]*>/);
    expect(h).toContain("Clear");
  });

  test("no Clear button at defaults", () => {
    const h = html(LibraryFilters, { filters: defaultFilters(), facets: null, onchange: noop });
    expect(h).not.toContain(">Clear<");
  });

  test("the new filters alone make the form dirty", () => {
    const h = html(LibraryFilters, { filters: { ...defaultFilters(), lossy: false }, facets: null, onchange: noop });
    expect(h).toContain(">Clear<");
  });
});

describe("TrackTable chips (M2c)", () => {
  const rows = (...tracks: unknown[]) => html(TrackTable, { tracks, onplay: noop });

  test("recipe_known false shows Recipe unknown with a title", () => {
    const h = rows(summaryReference);
    expect(h).toContain("Recipe unknown");
    expect(h).toMatch(/title="[^"]*cannot be re-rendered or used as a mother/);
    expect(h).not.toContain("Lossy MP3");
  });

  test("lossy shows Lossy MP3 with a title", () => {
    const h = rows(summaryLossy);
    expect(h).toContain("Lossy MP3");
    expect(h).toMatch(/title="[^"]*no lossless file/);
    expect(h).toContain("Recipe unknown");
  });

  test("known and lossless has no chip", () => {
    const h = rows(summaryKnown);
    expect(h).not.toContain("Recipe unknown");
    expect(h).not.toContain("Lossy MP3");
  });
});

describe("duel chips (M2c)", () => {
  test("a blind duel of lossy tracks shows neither chip before the verdict", () => {
    const h = html(DuelCard, { duel: duelOpenBlindLossy, onsubmit: async () => duelRevealedLossy });
    expect(h).not.toContain("Lossy MP3");
    expect(h).not.toContain("Recipe unknown");
  });

  test("the reveal shows both chips per lossy track", () => {
    const h = html(DuelReveal, { duel: duelRevealedLossy });
    expect(h.match(/Lossy MP3/g)?.length).toBe(2);
    expect(h.match(/Recipe unknown/g)?.length).toBe(2);
  });
});
