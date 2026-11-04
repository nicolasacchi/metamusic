import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import type { components } from "../../api/schema";
import Track from "../../routes/Track.svelte";
import { trackKnown, trackLossy, trackReference } from "../fixtures/m2c";
import HeatStrip from "./HeatStrip.svelte";
import PatchView from "./PatchView.svelte";
import SongPrPanel from "./SongPrPanel.svelte";

type S = components["schemas"];
const html = (c: unknown, props: Record<string, unknown>) => render(c as never, { props } as never).body;

const fix = (id: string, o: Partial<S["Comment"]> = {}): S["Comment"] =>
  ({ id, track_id: "t1", kind: "fix", state: "open", hidden: false, at_second: 72, body: "the hook is thin", section: { label: "chorus 1", confidence: 0.7, source: "plan" }, ...o }) as S["Comment"];
const sections = [
  { idx: 0, kind: "verse", label: "verse 1", lines: ["a one", "a two"] },
  { idx: 1, kind: "chorus", label: "chorus 1", lines: ["c one"] },
] as unknown as S["SectionOut"][];
const principal = { kind: "human", id: "h", name: "operator" };
const patchSummary = {
  id: "p1", title: "Fatter hook", rationale: "r", operator: "lyric_edit", author: principal, created_at: "2026-11-04T10:00:00Z",
  base: { track_id: "t1", title: "Base", recipe_hash: "h" },
  renders: [
    { batch_id: "b1", job_id: "j1", seed: 1, state: "done", child_track_id: "c1" },
    { batch_id: "b1", job_id: "j2", seed: 2, state: "done", child_track_id: "c2" },
  ],
  addresses: [], callbacks: { total: 2, answered: 1, fixed: 1, not_yet: 0, worse: 0, kept: 0, lost: 0 },
} as unknown as S["SongPRSummary"];

describe("SongPrPanel", () => {
  test("lists open fix comments with spots, hides the rest", () => {
    const h = html(SongPrPanel, { trackId: "t1", comments: [fix("a"), fix("b", { kind: "note" }), fix("c", { state: "resolved" })], initial: { state: "ready", value: { items: [], total: 0, next_cursor: null } } });
    expect((h.match(/data-testid="song-pr-fix"/g) ?? []).length).toBe(1);
    expect(h).toContain("1:12 · chorus 1 · ~70%");
    expect(h).toContain("the hook is thin");
  });
  test("no fix comments says how to start; no propose button", () => {
    const h = html(SongPrPanel, { trackId: "t1", comments: [], canPropose: true, initial: { state: "ready", value: { items: [], total: 0, next_cursor: null } } });
    expect(h).toContain('data-testid="song-pr-no-fixes"');
    expect(h).not.toContain('data-testid="song-pr-open"');
  });
  test("the propose button is for the human only", () => {
    const base = { trackId: "t1", comments: [fix("a")], sections, initial: { state: "ready", value: { items: [], total: 0, next_cursor: null } } };
    expect(html(SongPrPanel, { ...base, canPropose: true })).toContain('data-testid="song-pr-open"');
    expect(html(SongPrPanel, { ...base, canPropose: false })).not.toContain('data-testid="song-pr-open"');
  });
  test("the patch list shows renders and callback tallies", () => {
    const h = html(SongPrPanel, { trackId: "t1", comments: [], initial: { state: "ready", value: { items: [patchSummary], total: 1, next_cursor: null } } });
    expect(h).toContain('href="/patches/p1"');
    expect(h).toContain("Fatter hook");
    expect(h).toContain("1 render ·");
    expect(h).toContain("1 of 2 answered · 1 fixed");
  });
  test("a stubbed (501/503) service shows 'not available yet' and no form", () => {
    const h = html(SongPrPanel, { trackId: "t1", comments: [fix("a")], canPropose: true, initial: { state: "unavailable" } });
    expect(h).toContain('data-testid="song-pr-unavailable"');
    expect(h).toContain("not available yet");
    expect(h).not.toContain('data-testid="song-pr-open"');
  });
  test("a list error is shown, not thrown", () => {
    expect(html(SongPrPanel, { trackId: "t1", initial: { state: "error", message: "boom" } })).toContain("boom");
  });
});

describe("PatchView", () => {
  const full = {
    ...patchSummary,
    ops: [], compiled: {},
    addresses: [{ comment_id: "a", track_id: "t1", kind: "fix", state: "open", body_excerpt: "the hook is thin" }],
    bounds: { bounds: { lines_changed: 1, max_lines: 8, sections_scoped: 1, max_sections: 4, style_terms_changed: 1, max_style_terms: 3, bpm_delta: 0, max_bpm_delta: 20 }, sections: [{ idx: 1, kind: "chorus", label: "chorus 1", extra: false }] },
    diff: { lyrics: [{ section_idx: 1, line_idx: 0, before: "old line", after: "new line" }], style: { before: "pop", after: "pop, dark", added: ["dark"], removed: [] } },
  } as unknown as S["SongPR"];
  const props = { id: "p1", navigate: () => {}, viewer: { kind: "human", scopes: ["generate"] } };
  test("shows addresses, scope, diff, renders and tally", () => {
    const h = html(PatchView, { ...props, initial: { state: "ready", value: full } });
    for (const t of ["patch-addresses", "patch-scope", "patch-diff", "patch-renders", "patch-tally"]) expect(h).toContain(`data-testid="${t}"`);
    expect(h).toContain("old line");
    expect(h).toContain("new line");
    expect(h).toContain("+ dark");
    expect(h).toContain("1/8 lines");
    expect(h).toContain("chorus 1");
    expect(h).toContain("seed 2 · done");
    expect(h).toContain("1 of 2 answered · 1 fixed");
    expect(h).toContain('data-testid="patch-render-button"');
  });
  test("the render button needs the generate scope and a human or agent, not a device", () => {
    const ready = { state: "ready", value: full };
    expect(html(PatchView, { ...props, viewer: { kind: "human", scopes: ["read"] }, initial: ready })).not.toContain("patch-render-button");
    expect(html(PatchView, { ...props, viewer: { kind: "device", scopes: ["generate"] }, initial: ready })).not.toContain("patch-render-button");
  });
  test("at the render limit the button is disabled and says why", () => {
    const three = { ...full, renders: ["b1", "b2", "b3"].map((b, i) => ({ batch_id: b, job_id: `j${i}`, seed: i, state: "done", child_track_id: null })) };
    const h = html(PatchView, { ...props, initial: { state: "ready", value: three } });
    expect(h).toMatch(/data-testid="patch-render-button"[^>]*disabled|disabled[^>]*data-testid="patch-render-button"/);
    expect(h).toContain("patch-render-limit");
  });
  test("501 and 503 show 'not available yet'; an error shows its message", () => {
    expect(html(PatchView, { ...props, initial: { state: "unavailable" } })).toContain("not available yet");
    expect(html(PatchView, { ...props, initial: { state: "error", message: "Patch not found" } })).toContain("Patch not found");
  });
});

describe("HeatStrip", () => {
  const heat = { track_id: "t1", duration_s: 120, bins: 4, bin_s: 30, sessions: 2, plays: 3, completion_rate: 0.5, heard: [2, 1, 0, 0], replays: [0, 2, 0, 0], rewinds: [0, 1, 0, 0], skips: [0, 0, 0, 1], cold: [3], hotspots: [{ from_s: 30, to_s: 60, score: 5, label: "chorus 1" }] };
  test("draws the bands and a hotspot chip", () => {
    const h = html(HeatStrip, { trackId: "t1", duration: 120, initial: { state: "ready", value: heat } });
    expect(h).toContain('data-testid="heat-bands"');
    expect(h).toContain("0:30–1:00 · chorus 1");
    expect(h).toContain('aria-label="Listening heat: 2 sessions, 50% completed, 1 hotspot"');
  });
  test("no listening yet says so and has no chips", () => {
    const h = html(HeatStrip, { trackId: "t1", duration: 120, initial: { state: "ready", value: { ...heat, sessions: 0, hotspots: [], heard: [0, 0, 0, 0], replays: [0, 0, 0, 0], rewinds: [0, 0, 0, 0], skips: [0, 0, 0, 0] } } });
    expect(h).toContain("No listening recorded yet");
    expect(h).not.toContain("heat-hotspot");
  });
  test("501 shows 'not available yet'", () => {
    expect(html(HeatStrip, { trackId: "t1", duration: 120, initial: { state: "unavailable" } })).toContain("not available yet");
  });
});

describe("Track page mounts", () => {
  const page = (t: typeof trackKnown) => render(Track, { props: { id: t.id, initial: t } } as never).body;
  test("a known lossless take gets the Song PR panel and the heat strip", () => {
    const h = page(trackKnown);
    expect(h).toContain('data-testid="song-pr-panel"');
    expect(h).toContain('data-testid="heat-strip"');
  });
  test("reference and lossy takes cannot be patched", () => {
    expect(page(trackReference)).not.toContain('data-testid="song-pr-panel"');
    expect(page(trackLossy)).not.toContain('data-testid="song-pr-panel"');
  });
});
