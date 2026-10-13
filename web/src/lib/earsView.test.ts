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
});
