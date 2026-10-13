import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import { defaultFilters, isDirty, parseFilters, toApiQuery, toSearch, type Status } from "./libraryQuery";

describe("libraryQuery", () => {
  test("defaults are omitted", () => {
    expect(toSearch(defaultFilters())).toBe("");
    expect(parseFilters("")).toEqual(defaultFilters());
  });

  test("round-trips every filter, repeatable params as repeated keys", () => {
    const f = {
      ...defaultFilters(),
      q: "café noir",
      style: ["a", "b"],
      status: ["final", "draft"] as Status[],
      minDuration: 60,
      maxDuration: 240.5,
      recordedFrom: "2026-01-01",
      recordedTo: "2026-02-01",
      hasScore: true,
      truncated: false,
      sort: "duration_desc" as const,
    };
    const s = toSearch(f);
    expect(s.startsWith("?")).toBe(true);
    expect(s).toContain("style=a&style=b");
    expect(s).toContain("status=final&status=draft");
    expect(parseFilters(s)).toEqual(f);
  });

  test("relevance is the default sort only when q is set", () => {
    expect(toSearch({ ...defaultFilters(), q: "x", sort: "relevance" })).toBe("?q=x");
    expect(parseFilters("?q=x").sort).toBe("relevance");
    expect(toSearch({ ...defaultFilters(), q: "x", sort: "title_asc" })).toBe("?q=x&sort=title_asc");
    expect(parseFilters("?sort=title_asc").sort).toBe("title_asc");
    expect(toSearch({ ...defaultFilters(), sort: "recorded_asc" })).toBe("?sort=recorded_asc");
  });

  test("ignores malformed values and caps style at 5", () => {
    const f = parseFilters("?min_duration_s=abc&has_score=maybe&sort=bogus&status=nope&style=1&style=2&style=3&style=4&style=5&style=6");
    expect(f.minDuration).toBeNull();
    expect(f.hasScore).toBeNull();
    expect(f.sort).toBe("recorded_desc");
    expect(f.status).toEqual([]);
    expect(f.style).toHaveLength(5);
  });

  test("toApiQuery maps to search_tracks params", () => {
    const q = toApiQuery({ ...defaultFilters(), q: "x", style: ["a"], minDuration: 5, hasScore: true, sort: "title_asc" });
    expect(q).toEqual({ q: "x", style: ["a"], min_duration_s: 5, has_score: true, sort: "title_asc" });
    expect(toApiQuery(defaultFilters())).toEqual({});
  });

  test("the generated client serialises array query params as repeated keys", async () => {
    const seen: Request[] = [];
    const api = createApi({
      baseUrl: "http://localhost",
      fetch: async (req) => {
        seen.push(req);
        return new Response(JSON.stringify({ items: [], total: 0, next_cursor: null }), { headers: { "content-type": "application/json" } });
      },
    });
    await api.GET("/api/tracks", { params: { query: toApiQuery({ ...defaultFilters(), style: ["a", "b"], status: ["final", "draft"] as Status[] }) } });
    const u = new URL(seen[0]!.url);
    expect(u.searchParams.getAll("style")).toEqual(["a", "b"]);
    expect(u.search).toContain("style=a&style=b");
    expect(u.searchParams.getAll("status")).toEqual(["final", "draft"]);
  });
});

import { defaultSort as defaultSortFn, parseFilters as parse, toSearch as ts } from "./libraryQuery";
import { expect as ex, test as t2 } from "bun:test";

t2("defaultSort follows q; an explicit non-default sort survives in the URL", () => {
  ex(defaultSortFn("")).toBe("recorded_desc");
  ex(defaultSortFn("x")).toBe("relevance");
  const f = parse("?q=x&sort=recorded_desc");
  ex(f.sort).toBe("recorded_desc");
  ex(ts(f)).toBe("?q=x&sort=recorded_desc");
});

describe("libraryQuery judging filters", () => {
  test("min_stars, unrated, repeated status and tag round-trip", () => {
    const f = { ...defaultFilters(), minStars: 4, status: ["final"] as Status[], tag: ["a", "b"] };
    const s = toSearch(f);
    expect(s).toBe("?status=final&min_stars=4&tag=a&tag=b");
    expect(parseFilters(s)).toEqual(f);
    expect(toApiQuery(f)).toEqual({ status: ["final"], min_stars: 4, tag: ["a", "b"] });
  });
  test("unrated is exclusive with min_stars (unrated wins)", () => {
    const f = parseFilters("?min_stars=3&unrated=true");
    expect(f.unrated).toBe(true);
    expect(f.minStars).toBeNull();
    expect(toSearch(f)).toBe("?unrated=true");
    expect(toApiQuery(f)).toEqual({ unrated: true });
  });
  test("bad stars are ignored; stars_desc is a sort", () => {
    expect(parseFilters("?min_stars=9").minStars).toBeNull();
    expect(parseFilters("?min_stars=0").minStars).toBeNull();
    expect(parseFilters("?min_stars=2.5").minStars).toBeNull();
    expect(parseFilters("?sort=stars_desc").sort).toBe("stars_desc");
  });
  test("tags are deduplicated", () => {
    expect(parseFilters("?tag=a&tag=a&tag=b").tag).toEqual(["a", "b"]);
  });
});

describe("libraryQuery (M2c)", () => {
  test("recipe_known and lossy round-trip", () => {
    const f = parseFilters("?recipe_known=false&lossy=true");
    expect(f.recipeKnown).toBe(false);
    expect(f.lossy).toBe(true);
    expect(toSearch(f)).toBe("?recipe_known=false&lossy=true");
    expect(toApiQuery(f)).toEqual({ recipe_known: false, lossy: true });
    expect(parseFilters(toSearch(f))).toEqual(f);
    const k = parseFilters("?recipe_known=true&lossy=false");
    expect([k.recipeKnown, k.lossy]).toEqual([true, false]);
    expect(toApiQuery(k)).toEqual({ recipe_known: true, lossy: false });
  });

  test("absent and invalid values are null", () => {
    expect([defaultFilters().recipeKnown, defaultFilters().lossy]).toEqual([null, null]);
    const f = parseFilters("?recipe_known=maybe&lossy=1");
    expect([f.recipeKnown, f.lossy]).toEqual([null, null]);
    expect(toSearch(f)).toBe("");
    expect(toApiQuery(f)).toEqual({});
  });

  test("isDirty counts the two new filters", () => {
    expect(isDirty(defaultFilters())).toBe(false);
    expect(isDirty({ ...defaultFilters(), recipeKnown: false })).toBe(true);
    expect(isDirty({ ...defaultFilters(), lossy: false })).toBe(true);
    expect(isDirty({ ...defaultFilters(), hasScore: true })).toBe(true);
  });
});
