import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import EnergyBadge from "./components/EnergyBadge.svelte";
import KeyBadge from "./components/KeyBadge.svelte";
import NextActionList from "./components/NextActionList.svelte";
import { atlasHref, basisWord, energyText, hasFeatureValues, isHollow, keyText, museAtlasHref, shortList, songAtlasHref, subjectNoteText, tempoFeatureText, type NextActionItem } from "./featuresView";
import { filterFromSearch } from "./atlasView";

const act = (code: string, priority: number, text = `text ${code}`): NextActionItem => ({ code, priority, text, audience: "both", evidence: {}, subject: { kind: "song", id: "s1" } });

describe("featuresView", () => {
  test("basis words", () => {
    expect(basisWord("measured")).toBe("measured");
    expect(basisWord("plan")).toBe("from the plan");
    expect(basisWord(null)).toBe("");
    expect(basisWord("odd")).toBe("odd");
    expect(isHollow("predicted")).toBe(true);
    expect(isHollow("measured")).toBe(false);
    expect(isHollow("declared")).toBe(false);
  });
  test("key text", () => {
    expect(keyText({ camelot: "8A", name: "A minor", basis: "measured" })).toBe("8A · A minor · measured");
    expect(keyText({ camelot: "2B", name: "F# major", basis: "declared" })).toBe("2B · F# major · declared");
    expect(keyText({ camelot: "2B", name: null, basis: null })).toBe("2B");
    expect(keyText({ camelot: null, name: null, basis: null })).toBeNull();
    expect(keyText(null)).toBeNull();
  });
  test("energy text", () => {
    expect(energyText({ value: 0.53, basis: "measured" })).toBe("0.53 measured");
    expect(energyText({ value: 0.33, basis: "predicted" })).toBe("0.33 predicted");
    expect(energyText({ value: null, basis: null })).toBeNull();
  });
  test("tempo text and subject note", () => {
    const t = { bpm: 75, basis: "measured", measured_bpm: 75, octave: false, requested_bpm: 76 };
    expect(tempoFeatureText(t)).toBe("75 BPM · measured");
    expect(tempoFeatureText({ ...t, octave: true })).toBe("75 BPM · measured · octave-corrected");
    expect(tempoFeatureText({ ...t, bpm: null })).toBeNull();
    expect(subjectNoteText("all_takes_discarded")).toContain("Every take is discarded");
    expect(subjectNoteText(null)).toBe("");
  });
  test("hasFeatureValues", () => {
    expect(hasFeatureValues(null)).toBe(false);
    expect(hasFeatureValues({ key: { camelot: "8A", name: null, basis: null }, energy: { value: null, basis: null }, tempo: { bpm: null, basis: null, measured_bpm: null, octave: false, requested_bpm: null } } as never)).toBe(true);
  });
  test("atlas links round-trip through the page filter", () => {
    expect(songAtlasHref("s 1")).toBe("/atlas?near=s+1");
    expect(filterFromSearch(songAtlasHref("s 1").split("?")[1]!).near).toBe("s 1");
    expect(museAtlasHref("m1")).toBe("/atlas?muse=m1");
    expect(atlasHref({})).toBe("/atlas");
  });
  test("short list: priority order, stable, capped", () => {
    const items = [act("a", 1), act("b", 5), act("c", 5), act("d", 3), act("e", 2), act("f", 4), act("g", 0)];
    const r = shortList(items, 5);
    expect(r.shown.map((x) => x.code)).toEqual(["b", "c", "f", "d", "e"]);
    expect(r.more).toBe(2);
  });
});

describe("badges and list", () => {
  test("KeyBadge renders text, or nothing", () => {
    const h = render(KeyBadge, { props: { musicalKey: { camelot: "8A", name: "A minor", basis: "measured" } } } as never).body;
    expect(h).toContain("8A · A minor · measured");
    expect(h).not.toContain("hollow");
    expect(render(KeyBadge, { props: { musicalKey: null } } as never).body).not.toContain("key-badge");
  });
  test("EnergyBadge marks predicted hollow and measured solid", () => {
    const p = render(EnergyBadge, { props: { energy: { value: 0.33, basis: "predicted" } } } as never).body;
    expect(p).toContain("0.33 predicted");
    expect(p).toMatch(/class="[^"]*hollow/);
    const m = render(EnergyBadge, { props: { energy: { value: 0.53, basis: "measured" } } } as never).body;
    expect(m).toContain("0.53 measured");
    expect(m).not.toMatch(/class="[^"]*hollow/);
  });
  test("NextActionList: loading, empty state, items", () => {
    expect(render(NextActionList, { props: { items: null } } as never).body).toContain("Loading");
    expect(render(NextActionList, { props: { items: [] } } as never).body).toContain("Nothing suggested right now.");
    const h = render(NextActionList, { props: { items: [act("x", 2, "Judge the takes")] } } as never).body;
    expect(h).toContain("Judge the takes");
  });
});
