import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import {
  NON_SAVED,
  describeFilter,
  fieldKeys,
  filterFromForm,
  filterFromSearch,
  filterToQuery,
  filterToSearch,
  formFromFilter,
  mergeFilters,
  normalizeFilter,
  splitList,
  validateFilter,
} from "./crateFilter";

type Spec = { paths: Record<string, Record<string, { operationId?: string; parameters?: { name: string; in: string }[] }>> };
const spec = JSON.parse(readFileSync(new URL("../../../openapi.json", import.meta.url), "utf8")) as Spec;
function queryParams(operationId: string): string[] {
  for (const item of Object.values(spec.paths))
    for (const op of Object.values(item)) if (op.operationId === operationId) return (op.parameters ?? []).filter((p) => p.in === "query").map((p) => p.name);
  throw new Error("no such operation " + operationId);
}
const minusNonSaved = (names: string[]) => names.filter((n) => !(NON_SAVED as readonly string[]).includes(n)).sort();

describe("vocabulary equals the list-op query parameters (twin of TestCrateVocabulary_EqualsListOps)", () => {
  test("song = list_songs", () => expect(fieldKeys("song").sort()).toEqual(minusNonSaved(queryParams("list_songs"))));
  test("take = search_tracks", () => expect(fieldKeys("take").sort()).toEqual(minusNonSaved(queryParams("search_tracks"))));
  test("the L2 predicates are present", () => {
    for (const k of ["unheard", "in_record", "pick_decided"]) expect(fieldKeys("song")).toContain(k);
    for (const k of ["unheard", "in_record"]) expect(fieldKeys("take")).toContain(k);
    expect(fieldKeys("take")).not.toContain("pick_decided");
  });
  test("crate_id is never saved", () => {
    expect(fieldKeys("song")).not.toContain("crate_id");
    expect(fieldKeys("take")).not.toContain("crate_id");
  });
});

describe("the concept example", () => {
  test("builds exactly the documented filter", () => {
    const form = formFromFilter("song", {});
    form["sound"] = ["genre_family:reggae"];
    form["unheard"] = "true";
    form["min_duration_s"] = "240";
    form["pick_decided"] = "false";
    const f = filterFromForm("song", form);
    expect(JSON.stringify(f)).toBe('{"sound":["genre_family:reggae"],"unheard":true,"min_duration_s":240,"pick_decided":false}');
    expect(validateFilter("song", f)).toEqual({});
  });
  test("has_pick keeps its own meaning: it is not pick_decided", () => {
    const f = filterFromForm("song", { ...formFromFilter("song", {}), has_pick: "false" });
    expect(f).toEqual({ has_pick: false });
  });
});

describe("filter <-> query string", () => {
  const f = { state: ["rendered", "judged"], sound: ["genre_family:reggae", "mood:warm"], tag: ["a", "b"], unheard: true, min_duration_s: 240, pick_decided: false };
  test("repeated keys round trip", () => {
    const s = filterToSearch(normalizeFilter("song", f));
    expect(s).toContain("state=rendered&state=judged");
    expect(s).toContain("sound=genre_family%3Areggae&sound=mood%3Awarm");
    expect(filterFromSearch("song", s)).toEqual(normalizeFilter("song", f));
    expect(filterFromSearch("song", "?" + s)).toEqual(normalizeFilter("song", f));
  });
  test("unknown keys, bad numbers and non-saved parameters are ignored", () => {
    expect(filterFromSearch("song", "crate_id=x&limit=5&bogus=1&min_stars=abc&unheard=maybe")).toEqual({});
  });
  test("a key of the other subject is not read", () => {
    expect(filterFromSearch("take", "pick_decided=true&unheard=true")).toEqual({ unheard: true });
  });
  test("the list-op query keeps arrays and adds paging", () => {
    expect(filterToQuery({ tag: ["a"], unheard: true }, { limit: 1 })).toEqual({ tag: ["a"], unheard: true, limit: 1 });
  });
});

describe("form model", () => {
  test("empty form builds the empty filter; empty arrays are never saved", () => {
    expect(filterFromForm("take", formFromFilter("take", {}))).toEqual({});
    expect(normalizeFilter("song", { tag: [], q: "", unheard: false })).toEqual({ unheard: false });
  });
  test("a stored filter fills the form and rebuilds identically", () => {
    const f = { status: ["draft"], unheard: true, min_stars: 3 };
    const form = formFromFilter("take", f);
    expect(form["min_stars"]).toBe("3");
    expect(filterFromForm("take", form)).toEqual(f);
  });
  test("splitList trims and dedupes", () => expect(splitList(" a, b\nb ,, c")).toEqual(["a", "b", "c"]));
});

describe("client-side validation", () => {
  test("min_stars with unrated=true is blocked with the server's message", () => {
    expect(validateFilter("song", { min_stars: 3, unrated: true })).toEqual({ unrated: "cannot be combined with min_stars" });
    expect(validateFilter("song", { min_stars: 3, unrated: false })).toEqual({});
  });
  test("ranges, tag count, unknown keys, size", () => {
    expect(validateFilter("song", { min_stars: 6 })["min_stars"]).toBe("must be 1..5");
    expect(validateFilter("song", { min_duration_s: -1 })["min_duration_s"]).toBe("must not be negative");
    expect(validateFilter("song", { tag: ["1", "2", "3", "4", "5", "6"] })["tag"]).toBe("at most 5 tags");
    expect(validateFilter("song", { crate_id: "x" })["crate_id"]).toBe("unknown filter key");
    expect(validateFilter("take", { pick_decided: true })["pick_decided"]).toBe("unknown filter key");
    expect(validateFilter("song", { q: "x".repeat(5000) })["filter"]).toBe("at most 4096 bytes");
    expect(validateFilter("song", { min_duration_s: 300, max_duration_s: 200 })["max_duration_s"]).toBeDefined();
  });
});

describe("merge preview", () => {
  test("lists intersect, equal values stay, different scalars conflict", () => {
    const r = mergeFilters({ state: ["draft", "rendered"], unheard: true, min_duration_s: 240 }, { state: ["rendered"], unheard: true, min_duration_s: 100, q: "x" });
    expect(r.merged).toEqual({ state: ["rendered"], unheard: true, min_duration_s: 240, q: "x" });
    expect(r.conflicts).toEqual(["min_duration_s"]);
    expect(r.none).toBe(false);
  });
  test("an empty intersection matches nothing", () => expect(mergeFilters({ tag: ["a"] }, { tag: ["b"] }).none).toBe(true));
});

describe("describeFilter", () => {
  test("one phrase per key", () => {
    expect(describeFilter("song", { unheard: true, pick_decided: false, sound: ["genre_family:reggae"] })).toEqual([
      "Sound (facet:value): genre_family:reggae",
      "Unheard",
      "not: Pick decided (pinned or ladder)",
    ]);
  });
});
