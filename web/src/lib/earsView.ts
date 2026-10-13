// Ears panel model (C54.4): flags first, then metrics, then blind spots.
import type { components } from "../api/schema";

type Dossier = components["schemas"]["Dossier"];
type SpectrogramView = components["schemas"]["SpectrogramView"];

export interface FlagRow {
  code: string;
  label: string;
  severity: "info" | "warn";
  detail: string;
}
export interface MetricRow {
  label: string;
  value: string;
}
export interface PanelSection {
  id: "flags" | "metrics" | "blind-spots";
  title: string;
}
export interface EarsModel {
  sections: PanelSection[];
  flags: FlagRow[];
  metrics: MetricRow[];
  blindSpots: string[];
  notice: string | null;
  summary: string;
  spectrogram: { url: string; width: number; height: number; plot: SpectrogramView["plot"]; overlay: SpectrogramView["overlay"] } | null;
}

const LABELS: Record<string, string> = {
  clipping: "Clipping",
  internal_silence: "Internal silence",
  token_loop_suspected: "Token loop suspected",
};

function label(code: string): string {
  return LABELS[code] ?? code.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());
}

const num = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) ? v : null);

function detail(f: Dossier["flags"][number]): string {
  const e = f.evidence;
  const parts: string[] = [];
  const clipped = num(e["clipped_s"]);
  if (clipped !== null) parts.push(`${clipped.toFixed(1)} s clipped`);
  const internal = num(e["internal_s"]);
  if (internal !== null) parts.push(`${internal.toFixed(1)} s of silence`);
  const at = num(e["at_s"]);
  if (at !== null) parts.push(`at ${at.toFixed(1)} s`);
  const span = num(e["span_s"]);
  if (span !== null) parts.push(`for ${span.toFixed(1)} s`);
  const period = num(e["period_s"]);
  if (period !== null) parts.push(`period ${period.toFixed(2)} s`);
  return parts.join(", ");
}

export function earsPanel(d: Dossier, s: SpectrogramView | null): EarsModel {
  const flags = d.flags
    .map((f): FlagRow => ({ code: f.code, label: label(f.code), severity: f.severity, detail: detail(f) }))
    .sort((x, y) => (x.severity === y.severity ? 0 : x.severity === "warn" ? -1 : 1));
  const metrics: MetricRow[] = [];
  if (d.loudness) {
    metrics.push({ label: "Integrated loudness", value: `${d.loudness.integrated_lufs.toFixed(1)} LUFS` });
    metrics.push({ label: "True peak", value: `${d.loudness.true_peak_dbtp.toFixed(1)} dBTP` });
    metrics.push({ label: "Loudness range", value: `${d.loudness.lra_lu.toFixed(1)} LU` });
  }
  if (d.spectrum && d.spectrum.bandwidth_hz !== null) {
    metrics.push({ label: "Bandwidth", value: `${(d.spectrum.bandwidth_hz / 1000).toFixed(1)} kHz` });
  }
  if (d.clipping) metrics.push({ label: "Clipped", value: `${d.clipping.clipped_s.toFixed(1)} s` });
  if (d.silence) metrics.push({ label: "Internal silence", value: `${d.silence.internal_s.toFixed(1)} s` });
  const blind = [...d.blind_spots];
  for (const b of s?.blind_spots ?? []) if (!blind.includes(b)) blind.push(b);
  return {
    sections: [
      { id: "flags", title: "Flags" },
      { id: "metrics", title: "Metrics" },
      { id: "blind-spots", title: "What this cannot hear" },
    ],
    flags,
    metrics,
    blindSpots: blind,
    notice: d.complete ? null : `Analysis incomplete: missing ${d.missing.join(", ")}`,
    summary: d.summary,
    spectrogram: s ? { url: s.url, width: s.width, height: s.height, plot: s.plot, overlay: s.overlay } : null,
  };
}
