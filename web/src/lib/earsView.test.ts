import { describe, expect, test } from "bun:test";
import { dossier, spectrogramView } from "./fixtures/m2b";
import { earsPanel } from "./earsView";

describe("earsPanel", () => {
  test("sections come in the order flags, metrics, blind spots", () => {
    const p = earsPanel(dossier, spectrogramView);
    expect(p.sections.map((s) => s.id)).toEqual(["flags", "metrics", "blind-spots"]);
  });
  test("flags carry severity and a readable label; warnings first", () => {
    const d = structuredClone(dossier);
    d.flags = [
      { code: "internal_silence", severity: "info", evidence: { internal_s: 2.5 } },
      { code: "clipping", severity: "warn", evidence: { clipped_s: 2 } },
    ];
    const p = earsPanel(d, null);
    expect(p.flags.map((f) => f.code)).toEqual(["clipping", "internal_silence"]);
    expect(p.flags[0]!.label).toContain("Clipping");
    expect(p.flags[0]!.severity).toBe("warn");
  });
  test("metrics list loudness, peak, bandwidth, silence and clipping", () => {
    const p = earsPanel(dossier, null);
    const m = Object.fromEntries(p.metrics.map((r) => [r.label, r.value]));
    expect(m["Integrated loudness"]).toBe("-14.0 LUFS");
    expect(m["True peak"]).toBe("-1.2 dBTP");
    expect(m["Bandwidth"]).toBe("16.0 kHz");
    expect(m["Clipped"]).toBe("2.0 s");
    expect(m["Internal silence"]).toBe("2.5 s");
  });
  test("missing analyzers are skipped, not shown as zero", () => {
    const d = structuredClone(dossier);
    d.clipping = null;
    d.silence = null;
    d.loudness = null;
    const labels = earsPanel(d, null).metrics.map((r) => r.label);
    expect(labels).not.toContain("Clipped");
    expect(labels).not.toContain("Integrated loudness");
  });
  test("blind spots include the spectrogram's own", () => {
    const p = earsPanel(dossier, spectrogramView);
    expect(p.blindSpots).toContain("timbre");
    expect(p.blindSpots.at(-1)).toContain("spectrogram shows energy");
    expect(new Set(p.blindSpots).size).toBe(p.blindSpots.length);
  });
  test("spectrogram model carries url and plot for seeking", () => {
    const p = earsPanel(dossier, spectrogramView);
    expect(p.spectrogram?.url).toBe(spectrogramView.url);
    expect(p.spectrogram?.plot.duration_s).toBe(12);
    expect(earsPanel(dossier, null).spectrogram).toBeNull();
  });
  test("an incomplete dossier says what is missing", () => {
    const d = structuredClone(dossier);
    d.complete = false;
    d.missing = ["clipping"];
    expect(earsPanel(d, null).notice).toContain("clipping");
    expect(earsPanel(dossier, null).notice).toBeNull();
  });
  test("the listening check flags have readable labels and details", () => {
    const d = structuredClone(dossier);
    d.flags = [
      { code: "diction_poor", severity: "warn", evidence: { section: "chorus", cer: 0.46 } },
      { code: "melody_off_plan", severity: "warn", evidence: { melody_chroma: 0.31, sections: 3 } },
      { code: "vocals_not_detected", severity: "info", evidence: { words: 2, lyric_words: 40 } },
      { code: "listening_check_failed", severity: "info", evidence: { state: "error", code: "ASR_FAILED" } },
      { code: "sung_transposed", severity: "info", evidence: { transpose_mode: 2, sections: 4 } },
      { code: "alignment_failed", severity: "info", evidence: { ref_words: 40 } },
    ];
    const f = Object.fromEntries(earsPanel(d, null).flags.map((x) => [x.code, x]));
    expect(f["diction_poor"]!.label).toBe("Diction may be poor");
    expect(f["diction_poor"]!.detail).toBe("chorus: 46 % of characters wrong");
    expect(f["melody_off_plan"]!.detail).toBe("melody match 31 %");
    expect(f["vocals_not_detected"]!.detail).toBe("2 of 40 lyric words heard");
    expect(f["listening_check_failed"]!.detail).toBe("state error");
    expect(f["sung_transposed"]!.detail).toBe("2 semitones");
    expect(f["alignment_failed"]!.label).toBe("Words could not be placed in time");
  });
});
