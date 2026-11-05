import { describe, expect, test } from "bun:test";
import type { EngineCaps } from "./engines";
import type { GenerationIntent } from "./genIntent";
import { buildPanel, panelFromIntent, parseSeconds, type PanelState } from "./modePanels";

const base = (over: Partial<GenerationIntent> = {}): GenerationIntent => ({ mode: "port", engine: "acestep15", parent_track_id: "t1", ...over });
const st = (over: Partial<PanelState> = {}): PanelState => ({ ...panelFromIntent(base()), ...over });
const caps = { repaint_min_s: 3, repaint_max_s: 30, extend_min_s: 3, extend_max_s: 90, reference_max_s: 30 } as EngineCaps;

describe("parseSeconds", () => {
  test("rounds half away from zero to 0.1 s", () => {
    expect(parseSeconds("12.34")).toBe(12.3);
    expect(parseSeconds("12.35")).toBe(12.4);
    expect(parseSeconds("20.0")).toBe(20);
    expect(parseSeconds("0")).toBe(0);
  });
  test("rejects non numbers and out of range", () => {
    for (const t of ["", "-1", "abc", "601", "1e2", "1,5"]) expect(parseSeconds(t)).toBeNull();
    expect(parseSeconds("600")).toBe(600);
  });
});

describe("buildPanel exact intents", () => {
  test("repaint 12.34 to 20.0 gives edit {from_s: 12.3, to_s: 20}", () => {
    const r = buildPanel("repaint", base({ mode: "repaint" }), st({ from: "12.34", to: "20.0" }), null);
    expect(r).toEqual({ ok: true, intent: { mode: "repaint", engine: "acestep15", parent_track_id: "t1", n_seeds: 1, edit: { from_s: 12.3, to_s: 20 } } });
  });
  test("extend 30 gives edit {extend_s: 30}", () => {
    const r = buildPanel("extend", base({ mode: "extend" }), st({ extend: "30" }), null);
    expect(r.ok && r.intent.edit).toEqual({ extend_s: 30 });
  });
  test("stems [vocals, drums] gives edit {tracks}", () => {
    const r = buildPanel("stems", base({ mode: "stems" }), st({ tracks: ["vocals", "drums"] }), null);
    expect(r.ok && r.intent.edit).toEqual({ tracks: ["vocals", "drums"] });
  });
  test("complete caps the names at four", () => {
    const r = buildPanel("complete", base({ mode: "complete" }), st({ tracks: ["vocals", "drums", "bass", "synth", "fx"] }), null);
    expect(r).toEqual({ ok: false, error: "Pick at most 4 track names." });
  });
  test("port sends only the filled patch fields", () => {
    const r = buildPanel("port", base(), st({ style: " warm pads ", bpm: "96", keyscale: "A minor", timesignature: "3", duration: "61.25", think: true, seedsText: "7, 9" }), null);
    expect(r).toEqual({
      ok: true,
      intent: {
        mode: "port",
        engine: "acestep15",
        parent_track_id: "t1",
        seeds: [7, 9],
        patch: { style: "warm pads", cot: "think", bpm: 96, keyscale: "A minor", timesignature: "3", duration_s: 61.3 },
      },
    });
    const bare = buildPanel("port", base(), st(), null);
    expect(bare.ok && bare.intent.patch).toBeUndefined();
  });
  test("reference carries the take and the window, or the default", () => {
    const b: GenerationIntent = { mode: "reference", engine: "acestep15", reference: { track_id: "r1" } };
    const w = buildPanel("reference", b, st({ from: "4", to: "14.5" }), null);
    expect(w.ok && w.intent.reference).toEqual({ track_id: "r1", from_s: 4, to_s: 14.5 });
    const d = buildPanel("reference", b, st(), null);
    expect(d.ok && d.intent.reference).toEqual({ track_id: "r1" });
    expect(buildPanel("reference", b, st({ from: "4" }), null).ok).toBe(false);
  });
});

describe("buildPanel limits and errors", () => {
  test("caps limits give early messages", () => {
    const rep = (from: string, to: string) => buildPanel("repaint", base({ mode: "repaint" }), st({ from, to }), caps);
    expect(rep("1", "2")).toEqual({ ok: false, error: "The repainted range must be at least 3 s (it is 1 s)." });
    expect(rep("0", "31")).toMatchObject({ ok: false });
    expect(rep("0", "30").ok).toBe(true);
    expect(buildPanel("extend", base({ mode: "extend" }), st({ extend: "91" }), caps).ok).toBe(false);
    const ref: GenerationIntent = { mode: "reference", engine: "acestep15", reference: { track_id: "r" } };
    expect(buildPanel("reference", ref, st({ from: "0", to: "31" }), caps).ok).toBe(false);
  });
  test("bad input", () => {
    const rep = (over: Partial<PanelState>) => buildPanel("repaint", base({ mode: "repaint" }), st(over), null);
    expect(rep({ from: "", to: "5" }).ok).toBe(false);
    expect(rep({ from: "9", to: "5" })).toEqual({ ok: false, error: "The range must end after it starts." });
    expect(buildPanel("port", base(), st({ bpm: "20" }), null).ok).toBe(false);
    expect(buildPanel("port", base(), st({ seedsText: "1 1" }), null).ok).toBe(false);
    expect(buildPanel("port", base(), st({ nSeeds: "0" }), null).ok).toBe(false);
    expect(buildPanel("stems", base({ mode: "stems" }), st(), null).ok).toBe(false);
  });
  test("panelFromIntent restores a filled intent", () => {
    const i = base({ mode: "repaint", edit: { from_s: 1.5, to_s: 9 }, n_seeds: 2 });
    expect(panelFromIntent(i)).toMatchObject({ from: "1.5", to: "9", nSeeds: "2" });
  });
});
