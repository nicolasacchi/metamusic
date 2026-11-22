import { beforeAll, describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import KaraokeLyrics from "./KaraokeLyrics.svelte";
import RecipeUnknownPanel from "./RecipeUnknownPanel.svelte";
import Diction from "../../routes/Diction.svelte";
import { me } from "../me.svelte";
import type { Page } from "../karaoke";

// The karaoke view, the Recover flow and the Diction Lab page, rendered with data (invented Italian lines only).

const clean = (h: string) => h.replace(/<!--.*?-->/g, "");
const word = (i: number, text: string, start: number | null, match = "exact", heard = "") => ({ i, text, start_s: start, end_s: start === null ? null : start + 0.5, timing: start === null ? null : "heard", match, heard });
const page = (): Page =>
  ({
    track_id: "t1",
    basis: "asr",
    lang: "it",
    thresholds: { clear: 0.15, mixed: 0.4 },
    lexicon_hits: [{ word: "pane", typical_heard: [], attempts: 4, mangled: 3 }],
    truncated: false,
    next_cursor: "n1",
    lines: [
      { section_idx: 0, line_idx: 0, text: "il pane caldo", start_s: 1, end_s: 3, timing: "heard", cer: 0.3, class: "mixed", class_source: "asr", mark: { human: null, critic: null }, words: [word(0, "il", 1), word(1, "pane", 1.5, "miss", "pene"), word(2, "caldo", 2)] },
      { section_idx: 0, line_idx: 1, text: "la luna bassa", start_s: 3, end_s: 5, timing: "estimated", cer: null, class: "unknown", class_source: "asr", mark: { human: null, critic: null }, words: [] },
      { section_idx: 1, line_idx: 0, text: "dormi ancora", start_s: 6, end_s: 8, timing: "heard", cer: 0.05, class: "unclear", class_source: "human", mark: { human: "unclear", critic: null }, words: [] },
    ],
  }) as Page;

describe("KaraokeLyrics", () => {
  beforeAll(() => {
    me.loaded = true;
    me.kind = "human";
    me.allowed = ["set_diction_marks", "get_track_diction"];
  });
  test("lines carry their class, source and timing; the playhead picks line and word; estimated lines have no word spans", () => {
    const h = clean(render(KaraokeLyrics as never, { props: { trackId: "t1", current: 1.6, onseek: () => {}, initial: page() } } as never).body);
    expect(h).toContain('data-class="mixed"');
    expect(h).toContain('data-source="human"');
    expect(h).toContain('data-timing="estimated"');
    expect(h.match(/data-active="true"/g)?.length).toBe(1);
    expect(h).toMatch(/class="w m-miss now"|class="[^"]*now[^"]*m-miss|m-miss[^"]*now/);
    expect(h).toContain("la luna bassa");
    expect(h).toContain('data-testid="karaoke-hits"');
    expect(h).toContain('data-testid="karaoke-more"');
    expect(h).toContain('aria-pressed="true"');
  });
  test("the Diction heading shows only with a loaded page", () => {
    const withPage = clean(render(KaraokeLyrics as never, { props: { trackId: "t1", current: null, onseek: () => {}, initial: page() } } as never).body);
    expect(withPage).toContain(">Diction</h2>");
    const without = clean(render(KaraokeLyrics as never, { props: { trackId: "t1", current: null, onseek: () => {} } } as never).body);
    expect(without).not.toContain(">Diction</h2>");
  });
  test("a caller who may not mark sees no mark buttons, only the mark", () => {
    me.allowed = ["get_track_diction"];
    const h = clean(render(KaraokeLyrics as never, { props: { trackId: "t1", current: null, onseek: () => {}, initial: page() } } as never).body);
    expect(h).not.toContain("mark-clear");
    expect(h).toContain("marked unclear");
    expect(h).not.toContain('data-active="true"');
  });
});

describe("RecipeUnknownPanel recover", () => {
  const panel = (props: object) => clean(render(RecipeUnknownPanel as never, { props: { title: "Alice", lossy: false, masterBytes: 1, durationS: 1, trackId: "r1", ...props } } as never).body);
  test("no check yet: Analyze, no draft button; a stored ok check is not asked here, so it waits", () => {
    me.allowed = ["recover_recipe", "estimate_generation"];
    const h = panel({ dossier: { tier: "0", flags: [] } });
    expect(h).toContain("recover-needs-check");
    expect(h).toContain("analyze-button");
    expect(h).not.toContain("recover-button");
    expect(panel({ dossier: { tier: "B", flags: [] } })).toContain("Checking what is known");
    expect(panel({ dossier: null })).toContain("Checking what is known");
  });
  test("a caller who cannot recover sees only the explanation", () => {
    me.allowed = ["estimate_generation"];
    expect(panel({ dossier: { tier: "0", flags: [] } })).not.toContain('data-testid="recover"');
    expect(panel({ trackId: "" })).toContain("Recipe unknown");
  });
});

describe("Diction page", () => {
  const entry = (word: string, over: object = {}) => ({ lang: "it", word, attempts: 6, mangled: 4, dropped: 1, rate: 0.667, typical_heard: [{ text: "pene", n: 3 }], tracks: 2, last_seen_at: "2026-11-20T10:00:00Z", status: "active", note: null, risk: true, examples: [{ track_id: "t9", at_second: 61.2, heard: "pene" }], ...over });
  const data = { entries: [entry("pane"), entry("luna", { status: "ignored", risk: false })], total: 2, next_cursor: null, calibration: { lines_marked: 0, tracks_marked: 0, clear: 0, unclear: 0, lang: "all" } };
  test("risky words are flagged, examples link to the moment, Ignore and Restore follow the status", () => {
    me.allowed = ["get_diction_lexicon", "update_lexicon_entry"];
    const h = clean(render(Diction as never, { props: { initial: data } } as never).body);
    expect(h).toContain('data-word="pane" data-risk="true"');
    expect(h).toContain('href="/tracks/t9?t=61"');
    expect(h).toContain("4 of 6 mangled (67 %)");
    expect(h).toContain("lex-ignore");
    expect(h).toContain("lex-restore");
    expect(h).toContain("No lines marked yet");
    me.allowed = ["get_diction_lexicon"];
    const ro = clean(render(Diction as never, { props: { initial: data } } as never).body);
    expect(ro).not.toContain("lex-ignore");
  });
  test("an empty lexicon says why", () => {
    const h = clean(render(Diction as never, { props: { initial: { ...data, entries: [], total: 0 } } } as never).body);
    expect(h).toContain("lex-empty");
  });
});
