import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import AtlasSelection from "./AtlasSelection.svelte";

const bar = (ids: string[], sheet = false) =>
  render(AtlasSelection, { props: { ids, boardHref: "/board?muse=m1", sheet, navigate: () => {}, onclear: () => {}, viewer: null } } as never).body;

describe("AtlasSelection", () => {
  test("is absent without a selection", () => {
    expect(bar([])).not.toContain("atlas-select-bar");
  });
  test("offers Crate, Record, Open (one Song only), Board and Clear", () => {
    const one = bar(["s1"]);
    expect(one).toContain("1 selected");
    expect(one).toContain('href="/songs/s1"');
    expect(one).toContain('href="/board?muse=m1"');
    expect(one).toContain('data-testid="atlas-crate-open"');
    expect(one).toContain('data-testid="atlas-record-open"');
    expect(bar(["s1", "s2"])).not.toContain("atlas-open-song");
  });
  test("a selection past 60 notes what the new Record leaves out", () => {
    const ids = Array.from({ length: 70 }, (_, i) => "s" + i);
    const h = bar(ids);
    expect(h).toContain("70 selected");
    expect(h).toContain("10 left out");
    expect(bar(["a", "b"])).not.toContain("atlas-record-cap");
  });
  test("on a phone it is a bottom sheet", () => {
    expect(bar(["s1"], true)).toMatch(/class="bar [^"]*\bsheet\b/);
  });
});
