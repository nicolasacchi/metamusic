import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import ChapterList from "./ChapterList.svelte";
import CurationToggles from "./CurationToggles.svelte";
import SettingsCuration from "../../routes/SettingsCuration.svelte";
import HumanCuration from "../../routes/HumanCuration.svelte";

const html = (c: unknown, props: Record<string, unknown>) => render(c as never, { props } as never).body;

describe("ChapterList", () => {
  const chapters = [
    { index: 2, slot_id: "b", start_ms: 61_500 },
    { index: 1, slot_id: "a", start_ms: 0 },
  ];
  test("ordered by index, named, with start times; the playing chapter is marked", () => {
    const h = html(ChapterList, { chapters, names: { a: "One" }, positionMs: 62_000, onselect() {} });
    expect(h.indexOf("One")).toBeLessThan(h.indexOf("Track 2"));
    expect(h).toContain("1:01");
    expect((h.match(/aria-current="true"/g) ?? []).length).toBe(1);
    expect(h.match(/<button[^>]*aria-current="true"[^>]*>[\s\S]*?Track 2/)).not.toBeNull();
  });
  test("no playhead: nothing marked", () => {
    expect(html(ChapterList, { chapters, onselect() {} })).not.toContain("aria-current");
  });
});

describe("CurationToggles", () => {
  const off = { export_muse_names: false, subsonic_muse_names: false };
  test("read-only: states and effects, no switch", () => {
    const h = html(CurationToggles, { settings: off });
    expect(h).toContain("never contain an inspiration's name");
    expect(h).toContain("automatic albums");
    expect(h).not.toContain('role="switch"');
    expect((h.match(/>Off</g) ?? []).length).toBe(2);
  });
  test("with onchange: one switch per key, reflecting the value", () => {
    const h = html(CurationToggles, { settings: { ...off, subsonic_muse_names: true }, onchange() {} });
    expect((h.match(/role="switch"/g) ?? []).length).toBe(2);
    expect(h).toContain('data-testid="curation-toggle-export_muse_names"');
    expect(h).toMatch(/aria-checked="true"[^>]*data-testid="curation-toggle-subsonic_muse_names"|data-testid="curation-toggle-subsonic_muse_names"[^>]*aria-checked="true"/);
  });
});

describe("curation pages", () => {
  test("render their shells (data loads in the browser)", () => {
    expect(render(SettingsCuration as never).body).toContain('data-testid="settings-curation-page"');
    expect(render(HumanCuration as never).body).toContain('data-testid="human-curation-page"');
  });
});
