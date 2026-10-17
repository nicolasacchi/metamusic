import { describe, expect, test } from "bun:test";
import {
  FACETS,
  MAPPABLE_FACETS,
  mappingOptions,
  orderedFacets,
  parseSound,
  selectedPills,
  soundsFromSearch,
  soundsToQuery,
  soundsToSearch,
  toggleSound,
  tokenLabel,
  validateMapping,
  visibleFacets,
} from "./soundsView";

describe("facet selection <-> sound= params", () => {
  test("toggle adds and removes a facet:value", () => {
    let s: string[] = [];
    s = toggleSound(s, "genre_family", "reggae");
    s = toggleSound(s, "genre_family", "dance");
    expect(s).toEqual(["genre_family:reggae", "genre_family:dance"]);
    s = toggleSound(s, "genre_family", "reggae");
    expect(s).toEqual(["genre_family:dance"]);
  });

  test("round trip keeps repeated keys, drops unknown facets, malformed and duplicate values", () => {
    const st = soundsFromSearch("?sound=era:90s&sound=vocal:male&sound=era:90s&sound=bogus:x&sound=nocolon&sound=mood:&tempo_mismatch=true&tab=terms");
    expect(st.sounds).toEqual(["era:90s", "vocal:male"]);
    expect(st.tempoMismatch).toBe(true);
    expect(st.tab).toBe("terms");
    expect(soundsToSearch(st)).toBe("sound=era%3A90s&sound=vocal%3Amale&tempo_mismatch=true&tab=terms");
    expect(soundsFromSearch(soundsToSearch(st))).toEqual(st);
  });

  test("defaults produce an empty query string", () => {
    expect(soundsToSearch(soundsFromSearch(""))).toBe("");
    expect(soundsFromSearch("")).toEqual({ sounds: [], tempoMismatch: false, tab: "songs" });
  });

  test("API query carries selections, the mismatch toggle and limit", () => {
    const q = soundsToQuery({ sounds: ["era:90s"], tempoMismatch: true, tab: "songs" }, { limit: 0 });
    expect(q).toEqual({ sound: ["era:90s"], tempo_mismatch: true, limit: 0 });
    expect(soundsToQuery({ sounds: [], tempoMismatch: false, tab: "songs" }, { cursor: "c1" })).toEqual({ cursor: "c1" });
  });

  test("selected pills show facet and value in facet order", () => {
    const pills = selectedPills(["vocal:male", "genre_family:reggae"]);
    expect(pills.map((p) => p.key)).toEqual(["genre_family:reggae", "vocal:male"]);
    expect(pills[0]!.label).toBe("Genre: reggae");
  });

  test("parseSound", () => {
    expect(parseSound("era:90s")).toEqual({ facet: "era", value: "90s" });
    expect(parseSound("tempo_band:slow")).toEqual({ facet: "tempo_band", value: "slow" });
    expect(parseSound("era")).toBeNull();
    expect(parseSound("x:y")).toBeNull();
  });
});

describe("counts rendering order", () => {
  test("facets render in the fixed facet order; values by count desc then value; empty facets are kept", () => {
    const out = orderedFacets({
      vocal: [
        { value: "male", count: 2 },
        { value: "female", count: 5 },
        { value: "duet", count: 2 },
      ],
      genre_family: [{ value: "pop", count: 1 }],
      unknown_facet: [{ value: "z", count: 9 }],
      mood: null,
    });
    expect(out.map((f) => f.facet)).toEqual([...FACETS]);
    expect(out.find((f) => f.facet === "vocal")!.values.map((v) => v.value)).toEqual(["female", "duet", "male"]);
    expect(out.find((f) => f.facet === "mood")!.values).toEqual([]);
  });
});

describe("Map… form validation per facet", () => {
  test("tempo_band and engine are not offered", () => {
    expect(MAPPABLE_FACETS).toEqual(["genre_family", "era", "vocal", "mood", "language"]);
    expect(mappingOptions().map((o) => o.facet)).not.toContain("tempo_band");
    expect(mappingOptions().map((o) => o.facet)).not.toContain("engine");
  });
  test("era regex", () => {
    for (const ok of ["50s", "90s", "00s", "20s"]) expect(validateMapping({ facet: "era", value: ok })).toBe("");
    for (const bad of ["30s", "1990s", "90", "90S", ""]) expect(validateMapping({ facet: "era", value: bad })).not.toBe("");
  });
  test("language is two lowercase letters", () => {
    expect(validateMapping({ facet: "language", value: "it" })).toBe("");
    for (const bad of ["ita", "i", "IT", "i1"]) expect(validateMapping({ facet: "language", value: bad })).not.toBe("");
  });
  test("other facets: slug up to 40 chars", () => {
    expect(validateMapping({ facet: "genre_family", value: "hip-hop" })).toBe("");
    expect(validateMapping({ facet: "mood", value: "a" + "b".repeat(39) })).toBe("");
    for (const bad of ["", "Pop", "-x", "1x", "a b", "a" + "b".repeat(40)]) expect(validateMapping({ facet: "mood", value: bad })).not.toBe("");
  });
  test("non-mappable facets are refused", () => {
    expect(validateMapping({ facet: "tempo_band", value: "slow" })).not.toBe("");
    expect(validateMapping({ facet: "engine", value: "yue2" })).not.toBe("");
  });
  test("term is required with a letter, 60 characters at most", () => {
    expect(validateMapping({ facet: "mood", value: "dark", term: "  " })).not.toBe("");
    expect(validateMapping({ facet: "mood", value: "dark", term: "123" })).not.toBe("");
    expect(validateMapping({ facet: "mood", value: "dark", term: "x".repeat(61) })).not.toBe("");
    expect(validateMapping({ facet: "mood", value: "dark", term: "gloomy" })).toBe("");
  });
});

describe("human labels and empty facets", () => {
  test("tokenLabel shows the facet's label, unknown tokens pass through", () => {
    expect(tokenLabel("genre_family:pop")).toBe("Genre: pop");
    expect(tokenLabel("tempo_band:upbeat")).toBe("Tempo: upbeat");
    expect(tokenLabel("bogus:x")).toBe("bogus:x");
    expect(tokenLabel("plain")).toBe("plain");
  });
  test("empty facets hide unless selected or shown all", () => {
    const blocks = orderedFacets({ genre_family: [{ value: "pop", count: 2 }], mood: [] });
    const names = (v: { shown: { facet: string }[] }) => v.shown.map((b) => b.facet);
    expect(names(visibleFacets(blocks, [], false))).toEqual(["genre_family"]);
    expect(visibleFacets(blocks, [], false).hidden).toBe(FACETS.length - 1);
    expect(names(visibleFacets(blocks, ["mood:dreamy"], false))).toEqual(["genre_family", "mood"]);
    expect(names(visibleFacets(blocks, [], true))).toHaveLength(FACETS.length);
  });
});
