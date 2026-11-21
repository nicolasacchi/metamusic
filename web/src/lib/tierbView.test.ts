import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import { adherenceLine, analyzeGate, canAnalyze, cerBars, loadsTierB, measuredNote, modelsLine, overallLine, stateText } from "./tierbView";

type TierB = components["schemas"]["TierB"];

const sec = (label: string, cer: number, ref_words = 10) => ({ label, cer, ref_words, line_count: 2, section: 0, wer: cer, word_edits: 1 });
const tb = (o: Partial<TierB> = {}): TierB =>
  ({
    state: "ok",
    status: "ok",
    code: null,
    language: "en",
    forced: true,
    flags: [],
    blind_spots: ["A recogniser can mishear sung words."],
    measured_sections: null,
    diction: { overall: { cer: 0.234, wer: 0.4, ref_words: 40, ref_runes: 200, char_edits: 47, word_edits: 16 }, sections: [sec("verse", 0.1), sec("chorus", 0.5), sec("outro", 0.9, 0)], worst_section: { label: "chorus", cer: 0.5 } },
    adherence: { basis: "coarse", key_relation: "exact", melody_chroma: 0.72, sections: [], tempo_ratio: 1, timing_source: "measured", transpose_mode: 0 },
    models: { asr: { name: "asr-small", compute_type: "int8", sha256: "x" }, pitch: { name: "pitch-a", sha256: "y" }, separator: { name: "sep-b", sha256: "z" } },
    ...o,
  }) as unknown as TierB;

describe("loadsTierB", () => {
  test("only a tier B dossier or a failed-check flag has a check to read", () => {
    expect(loadsTierB({ tier: "B", flags: [] })).toBe(true);
    expect(loadsTierB({ tier: "A", flags: [{ code: "listening_check_failed" }] })).toBe(true);
    expect(loadsTierB({ tier: "A", flags: [{ code: "clipping" }] })).toBe(false);
    expect(loadsTierB({ tier: "0", flags: [] })).toBe(false);
  });
});

describe("stateText and canAnalyze", () => {
  test("states in words", () => {
    expect(stateText("none")).toContain("No listening check");
    expect(stateText("ok")).toBe("Listened to the vocals");
    expect(stateText("weird")).toBe("weird");
  });
  test("analyze is offered for none and missing only", () => {
    expect(canAnalyze("none")).toBe(true);
    expect(canAnalyze("missing")).toBe(true);
    expect(canAnalyze("pending")).toBe(false);
    expect(canAnalyze("ok")).toBe(false);
    expect(canAnalyze("error")).toBe(false);
  });
});

describe("cerBars", () => {
  test("worst first, sections without words left out, percent capped", () => {
    const b = cerBars(tb());
    expect(b.map((x) => x.label)).toEqual(["chorus", "verse"]);
    expect(b[0]).toMatchObject({ pct: 50, text: "50 %" });
    expect(cerBars(tb({ diction: { overall: tb().diction.overall, sections: [sec("a", 1.7)], worst_section: tb().diction.worst_section } })).at(0)!.pct).toBe(100);
  });
  test("no diction, no bars", () => {
    expect(cerBars(tb({ diction: undefined as never }))).toEqual([]);
  });
});

describe("lines", () => {
  test("overall", () => {
    expect(overallLine(tb())).toBe("Overall: 23 % of characters wrong, 40 % of words, over 40 lyric words.");
    expect(overallLine(tb({ diction: undefined as never }))).toBe("");
  });
  test("measured note", () => {
    expect(measuredNote(tb())).toBe("No measured section times.");
    expect(measuredNote(tb({ measured_sections: [{ idx: 0, label: "v", start_s: 0, end_s: 5, confidence: 1 }] }))).toBe("Section times measured for 1 section.");
  });
  test("adherence is coarse and says how much", () => {
    expect(adherenceLine(tb())).toBe("Melody follows the plan about 72 % (coarse: pitch classes only).");
    expect(adherenceLine(tb({ adherence: { ...tb().adherence, melody_chroma: null } }))).toBe("Melody against the plan: not measured.");
    expect(adherenceLine(tb({ adherence: { ...tb().adherence, transpose_mode: 2 } }))).toContain("sung 2 semitones from the plan");
  });
  test("models", () => {
    expect(modelsLine(tb())).toBe("Heard with asr-small (int8), pitch tracker pitch-a, vocal separator sep-b.");
    const m = tb().models;
    expect(modelsLine(tb({ models: { ...m, pitch: null, separator: null } as never }))).toBe("Heard with asr-small (int8).");
    expect(modelsLine(tb({ models: { asr: null, pitch: null, separator: null } as never }))).toBe("No model ran.");
  });
});

describe("analyzeGate", () => {
  const all = ["v2", "ears", "analyze"];
  test("hidden without estimate_generation", () => {
    expect(analyzeGate([], all)).toEqual({ kind: "hidden" });
  });
  test("ready when the caps are open, or unknown (the server decides)", () => {
    expect(analyzeGate(["estimate_generation"], all)).toEqual({ kind: "ready" });
    expect(analyzeGate(["estimate_generation"], null)).toEqual({ kind: "ready" });
  });
  test("closed names what is missing", () => {
    const g = analyzeGate(["estimate_generation"], ["v2"]);
    expect(g.kind).toBe("closed");
    if (g.kind === "closed") {
      expect(g.reason).toContain("worker image is not open");
      expect(g.reason).toContain("ears, analyze");
    }
    expect(analyzeGate(["estimate_generation"], []).kind).toBe("closed");
  });
});
