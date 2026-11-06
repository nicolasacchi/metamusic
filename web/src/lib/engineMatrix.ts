// The /engines capability matrix: rows are capabilities, columns engines; read-only, from list_engines only.
import type { EngineList, EngineView } from "./engines";

export type Cell = { kind: "yes" | "no" | "unverified"; text: string };
export interface MatrixRow {
  key: string;
  label: string;
  cells: Cell[];
}

interface Def {
  key: string;
  label: string;
  /** Names the server may list in features.unverified for this row. */
  aliases: string[];
  has: (e: EngineView) => boolean;
  text?: (e: EngineView) => string;
}

const DEFS: Def[] = [
  { key: "text2song", label: "Text2Song", aliases: ["text2song", "text2music"], has: (e) => e.caps.text2song },
  { key: "tempo", label: "Tempo", aliases: ["tempo", "bpm"], has: (e) => e.caps.tempo },
  { key: "key", label: "Key", aliases: ["key", "keyscale"], has: (e) => e.caps.key },
  { key: "reference", label: "Reference", aliases: ["reference", "reference_audio"], has: (e) => e.caps.reference_audio, text: (e) => (e.caps.reference_max_s > 0 ? `up to ${e.caps.reference_max_s} s` : "") },
  { key: "repaint", label: "Repaint", aliases: ["repaint", "repaint_interval"], has: (e) => e.caps.repaint_interval, text: (e) => range(e.caps.repaint_min_s, e.caps.repaint_max_s) },
  { key: "extend", label: "Extend", aliases: ["extend"], has: (e) => e.caps.extend, text: (e) => range(e.caps.extend_min_s, e.caps.extend_max_s) },
  { key: "complete", label: "Complete", aliases: ["complete"], has: (e) => e.caps.complete },
  { key: "stems", label: "Stems", aliases: ["stems", "extract"], has: (e) => e.caps.stems, text: (e) => e.caps.stem_names.join(", ") },
  { key: "lrc", label: "LRC", aliases: ["lrc"], has: (e) => e.caps.lrc },
];

function range(min: number, max: number): string {
  if (min > 0 && max > 0) return `${min}–${max} s`;
  if (max > 0) return `up to ${max} s`;
  return "";
}

function cell(d: Def, e: EngineView): Cell {
  // The server lists a capability as unverified exactly when its effective cap is off (the verified-features file gates
  // the caps), so the unverified label has to win over "No" or it would never show.
  if (d.aliases.some((a) => e.features?.unverified?.includes(a))) return { kind: "unverified", text: "not yet verified" };
  if (!d.has(e)) return { kind: "no", text: "No" };
  return { kind: "yes", text: d.text?.(e) || "Yes" };
}

export function engineMatrix(list: EngineList): MatrixRow[] {
  const rows: MatrixRow[] = DEFS.map((d) => ({ key: d.key, label: d.label, cells: list.engines.map((e) => cell(d, e)) }));
  rows.push({
    key: "max_duration",
    label: "Max duration",
    cells: list.engines.map((e) => (e.caps.max_seconds > 0 ? { kind: "yes", text: `${e.caps.max_seconds} s` } : { kind: "unverified", text: "not yet verified" })),
  });
  return rows;
}

/** One line for an engine's adoption. */
export function adoptionText(e: EngineView): string {
  const a = e.adoption;
  if (a.state === "adopted") return `Adopted${a.via ? ` (${a.via})` : ""}`;
  if (a.state === "not_adopted") return "Not adopted";
  // no log row yet: an engine with an adopt hint has simply not been adopted; one without needs no adoption
  return e.adopt_hint ? "Not adopted" : "Not applicable";
}

/** One line for an engine's backend. */
export function backendText(e: EngineView): string {
  const b = e.backend;
  if (!b) return "No backend";
  if (!b.enabled) return `Disabled${b.disabled_reason ? ` (${b.disabled_reason.replaceAll("_", " ")})` : ""}`;
  if (b.tripped) return `Tripped${b.trip_reason ? ` (${b.trip_reason})` : ""}`;
  return `${b.armed ? "Armed" : "Disarmed"}, cold start ${b.cold_start_s} s`;
}
