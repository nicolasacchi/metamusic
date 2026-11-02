// View model of the Record studio's Sequence tab (C145.3): strategy labels, endpoint options, the `sequence` op,
// the preview table, coverage chip, cost bar, warnings in words, the energy curve and the stale badge.
// Pure functions; SequencePanel owns the markup and the calls.
import type { components } from "../api/schema";
import type { SlotOp } from "./recordView";

type S = components["schemas"];
export type Suggestion = S["Suggestion"];
export type Explanation = S["SequenceExplanation"];
export type Step = S["SequenceStep"];
export type Warning = S["SequenceWarning"];
export type Spec = S["SequenceSpec"];

export const STRATEGIES = ["arc", "ramp_up", "steady"] as const;
export type Strategy = (typeof STRATEGIES)[number];
export const DEFAULT_STRATEGY: Strategy = "arc";

const LABELS: Record<Strategy, string> = { arc: "Arc", ramp_up: "Ramp up", steady: "Steady" };
const HINTS: Record<Strategy, string> = {
  arc: "Builds to a peak about two thirds in, then eases off.",
  ramp_up: "Energy rises from start to finish.",
  steady: "Keeps energy level and moves smoothly in tempo and key.",
};

export const isStrategy = (s: string | null | undefined): s is Strategy => STRATEGIES.includes(s as Strategy);
export const strategyLabel = (s: string): string => (isStrategy(s) ? LABELS[s] : s.replace(/_/g, " "));
export const strategyHint = (s: string): string => (isStrategy(s) ? HINTS[s] : "");

/** `?sequence=arc` ⇒ the strategy to pre-select; a bare `?sequence` or an unknown value falls back to arc; absent ⇒ null (tab stays closed). */
export function strategyFromSearch(search: string): Strategy | null {
  const p = new URLSearchParams(search);
  if (!p.has("sequence")) return null;
  const v = p.get("sequence");
  return isStrategy(v) ? v : DEFAULT_STRATEGY;
}

export interface EndpointOption {
  id: string;
  title: string;
}

/** Start with / End with offer only the Songs the Record holds (each once, in slot order). */
export function endpointOptions(view: Pick<S["RecordView"], "slots">): EndpointOption[] {
  const seen = new Set<string>();
  const out: EndpointOption[] = [];
  for (const s of view.slots) {
    if (seen.has(s.song.id)) continue;
    seen.add(s.song.id);
    out.push({ id: s.song.id, title: s.song.title });
  }
  return out;
}

/** The sequence spec: empty endpoint values are left out. */
export function sequenceSpec(strategy: string, first?: string | null, last?: string | null): Spec {
  return { strategy, ...(first ? { first_song_id: first } : {}), ...(last ? { last_song_id: last } : {}) };
}

/** The `edit_record_slots` op for Apply. */
export function sequenceOp(strategy: string, first?: string | null, last?: string | null): SlotOp {
  return { op: "sequence", ...sequenceSpec(strategy, first, last) };
}

/** The whole `edit_record_slots` body for Apply: the revision the preview was made against travels with the op. */
export function sequenceEdit(revision: number, strategy: string, first?: string | null, last?: string | null): { revision: number; ops: SlotOp[] } {
  return { revision, ops: [sequenceOp(strategy, first, last)] };
}

/** Two endpoints may not name the same Song (the server answers SEQUENCE_ENDPOINTS_CONFLICT); the form says so first. */
export function endpointsConflict(first?: string | null, last?: string | null): boolean {
  return !!first && first === last;
}

export interface StepRow {
  position: number;
  songId: string;
  title: string;
  takeTitle: string | null;
  text: string;
  /** Plain-words reasons the server rendered; one sentence per step. */
  missing: string[];
  missingText: string;
  /** Relative energy 0..100 for the bar; null when the Song has no energy. */
  energyPct: number | null;
}

const MISSING_WORD: Record<string, string> = { bpm: "tempo", key: "key", energy: "energy" };

export function missingText(missing: readonly string[]): string {
  if (missing.length === 0) return "";
  return `no ${missing.map((m) => MISSING_WORD[m] ?? m).join(", ")}`;
}

export function stepRows(steps: readonly Step[]): StepRow[] {
  return steps.map((s) => ({
    position: s.position,
    songId: s.song_id,
    title: s.title,
    takeTitle: s.take_title ?? null,
    text: s.text,
    missing: s.missing,
    missingText: missingText(s.missing),
    energyPct: s.missing.includes("energy") ? null : Math.round(s.energy_rel / 100),
  }));
}

/** `tempo 6/6 · key 4/6 · energy 6/6 (2 predicted)`; the total is the number of steps. */
export function coverageText(c: Pick<S["SequenceCoverage"], "tempo" | "key" | "energy">, total: number, warnings: readonly Warning[] = []): string {
  const predicted = warnings.find((w) => w.code === "predicted_energy")?.n ?? 0;
  return `tempo ${c.tempo}/${total} · key ${c.key}/${total} · energy ${c.energy}/${total}${predicted > 0 ? ` (${predicted} predicted)` : ""}`;
}

export interface CostBar {
  input: string;
  output: string;
  /** Percent shown on the bar, 0..100; null when the server had no improvement figure. */
  pct: number | null;
  text: string;
}

export function costBar(c: S["SequenceCost"]): CostBar {
  const raw = c.improvement_pct === null ? NaN : Number(c.improvement_pct);
  const pct = Number.isFinite(raw) ? Math.min(100, Math.max(0, raw)) : null;
  let text: string;
  if (pct === null) text = `Transition cost ${c.input} → ${c.output}`;
  else if (raw <= 0) text = `Transition cost ${c.input} → ${c.output}: no better than the current order`;
  else text = `Transition cost ${c.input} → ${c.output}: ${c.improvement_pct}% smoother`;
  return { input: c.input, output: c.output, pct, text };
}

/** A preview is a no-op when the suggested order is the order the Record already has. */
export function isNoChange(s: Pick<Suggestion, "order" | "input_order">): boolean {
  return s.order.length === s.input_order.length && s.order.every((id, i) => id === s.input_order[i]);
}

/** Warnings in words; `titles` maps Song ids to titles for per-Song warnings. */
export function warningText(w: Warning, titles: ReadonlyMap<string, string> = new Map()): string {
  switch (w.code) {
    case "low_coverage": {
      const f = w.feature === "bpm" ? "a tempo" : w.feature === "key" ? "a key" : w.feature === "energy" ? "an energy value" : `a ${w.feature ?? "value"}`;
      return `Only ${w.known ?? 0} of ${w.total ?? 0} songs have ${f}, so that part of the order is a weak guide.`;
    }
    case "predicted_energy":
      return `${w.n ?? 0} ${w.n === 1 ? "song's energy is" : "songs' energy values are"} predicted, not measured.`;
    case "heuristic":
      return "A large set: the order is a good one, but not proven to be the best.";
    case "endpoint_cost":
      return "Fixing the first or last song makes the order a little rougher.";
    case "no_features":
      return `${(w.song && titles.get(w.song)) || "A song"} has no tempo, key or energy yet; it is placed where it disturbs least.`;
    case "single_song":
      return "Only one song: there is nothing to order.";
    case "pending_features":
      return "Some songs are still being analysed; preview again in a moment.";
    default:
      return w.code.replace(/_/g, " ");
  }
}

export function warningRows(ws: readonly Warning[], titles?: ReadonlyMap<string, string>): string[] {
  return ws.map((w) => warningText(w, titles));
}

/** Error codes of the sequence ops in words; null for codes this view does not know. */
export function sequenceErrorText(code: string): string | null {
  switch (code) {
    case "SEQUENCE_TOO_SMALL":
      return "A sequence needs at least two songs.";
    case "SEQUENCE_ENDPOINTS_CONFLICT":
      return "Start with and End with must be different songs.";
    case "SEQUENCE_SOURCE":
      return "Choose exactly one source to sequence.";
    case "UNKNOWN_SONG":
      return "One of the songs is not in this record.";
    case "SEQUENCE_ENDPOINT_NOT_IN_SET":
      return "Start with and End with must be songs of this record.";
    case "SEQUENCE_NO_FEATURES":
      return "None of the songs has a tempo, key or energy to order by yet.";
    default:
      return null;
  }
}

export interface Curve {
  width: number;
  height: number;
  /** The energy of the suggested order; null gaps are Songs without energy. */
  line: string;
  /** The shape the strategy aimed for; empty when it has none. */
  target: string;
  dots: { x: number; y: number; known: boolean }[];
}

/** SVG paths for the energy curve of a preview or run: x by position, y from relative energy (0 bottom). */
export function curveModel(steps: readonly Step[], width = 320, height = 100, pad = 8): Curve {
  const n = steps.length;
  const x = (i: number) => (n <= 1 ? width / 2 : pad + (i * (width - 2 * pad)) / (n - 1));
  const y = (v: number) => height - pad - (Math.min(10000, Math.max(0, v)) / 10000) * (height - 2 * pad);
  const path = (pts: ({ x: number; y: number } | null)[]) => {
    let d = "";
    let pen = false;
    for (const p of pts) {
      if (!p) {
        pen = false;
        continue;
      }
      d += `${pen ? "L" : "M"}${p.x.toFixed(1)} ${p.y.toFixed(1)}`;
      pen = true;
    }
    return d;
  };
  const known = steps.map((s, i) => (s.missing.includes("energy") ? null : { x: x(i), y: y(s.energy_rel) }));
  const target = steps.every((s) => s.target >= 0) && n > 0 ? path(steps.map((s, i) => ({ x: x(i), y: y(s.target) }))) : "";
  return {
    width,
    height,
    line: path(known),
    target,
    dots: steps.map((s, i) => ({ x: x(i), y: y(s.energy_rel), known: known[i] !== null })),
  };
}

/** The "who and when" line of the latest run. */
export function runText(e: Pick<Explanation, "actor" | "at" | "strategy">): string {
  const when = e.at.replace("T", " ").replace(/(\.\d+)?Z$/, " UTC");
  return `${strategyLabel(e.strategy)} order applied by ${e.actor.name} · ${when}`;
}

export const STALE_TEXT = "Order changed since";

/** The pinned slots: kept on their Take and side whatever the strategy decides. */
export interface PinnedRow {
  slotId: string;
  position: number;
  title: string;
  takeTitle: string;
  side: string | null;
}
export function pinnedRows(view: Pick<S["RecordView"], "slots">): PinnedRow[] {
  return view.slots
    .filter((s) => s.mode === "pinned" && s.state !== "empty")
    .map((s) => ({ slotId: s.id, position: s.position, title: s.song.title, takeTitle: s.take?.title ?? "", side: s.side }));
}
