// The listening check panel's model (M5b C347.1): state in words, per-section CER bars (worst first), the measured-sections note,
// the coarse adherence line, the models line and the Analyze gate. Pure.
import type { components } from "../api/schema";

type TierB = components["schemas"]["TierB"];

export interface CerBar {
  label: string;
  /** 0..100, the bar width. */
  pct: number;
  text: string;
  words: number;
}

const STATE_TEXT: Record<string, string> = {
  none: "No listening check yet: this take was not rendered with one.",
  pending: "A listening check is on its way.",
  missing: "The listening check never arrived for this take.",
  ok: "Listened to the vocals",
  error: "The listening check failed.",
  skipped: "The recogniser did not run, so nothing was listened to.",
};

export function stateText(state: string): string {
  return STATE_TEXT[state] ?? state;
}

/** Analyze is offered where a check is absent: none, or missing (a render that needed it and got nothing). */
export function canAnalyze(state: string): boolean {
  return state === "none" || state === "missing";
}

/**
 * Whether the dossier says a listening check is stored, so get_track_tier_b answers 200 (tier B, or the failed-check flag). Anything
 * else means none or pending, which that operation answers 409: the panel does not ask.
 */
export function loadsTierB(d: { tier: string; flags: readonly { code: string }[] }): boolean {
  return d.tier === "B" || d.flags.some((f) => f.code === "listening_check_failed");
}

const pct = (v: number): number => Math.max(0, Math.round(v * 100));

export function cerBars(t: TierB): CerBar[] {
  const secs = t.diction?.sections ?? [];
  return secs
    .filter((s) => s.ref_words > 0)
    .map((s) => ({ label: s.label, pct: Math.min(100, pct(s.cer)), text: `${pct(s.cer)} %`, words: s.ref_words }))
    .sort((a, b) => b.pct - a.pct);
}

export function overallLine(t: TierB): string {
  const o = t.diction?.overall;
  if (!o) return "";
  return `Overall: ${pct(o.cer)} % of characters wrong, ${pct(o.wer)} % of words, over ${o.ref_words} lyric words.`;
}

export function measuredNote(t: TierB): string {
  const n = t.measured_sections?.length ?? 0;
  return n === 0 ? "No measured section times." : `Section times measured for ${n} section${n === 1 ? "" : "s"}.`;
}

export function adherenceLine(t: TierB): string {
  const a = t.adherence;
  if (!a || a.melody_chroma === null) return "Melody against the plan: not measured.";
  const shift = a.transpose_mode ? `, sung ${a.transpose_mode} semitones from the plan` : "";
  return `Melody follows the plan about ${pct(a.melody_chroma)} % (coarse: pitch classes only)${shift}.`;
}

/** The pinned models that ran. Each is null in the answer when its stage did not run (the generated types call them required). */
export function modelsLine(t: TierB): string {
  const m = t.models as { asr: TierB["models"]["asr"] | null; pitch: TierB["models"]["pitch"] | null; separator: TierB["models"]["separator"] | null } | null;
  const parts: string[] = [];
  if (m?.asr) parts.push(`${m.asr.name} (${m.asr.compute_type})`);
  if (m?.pitch) parts.push(`pitch tracker ${m.pitch.name}`);
  if (m?.separator) parts.push(`vocal separator ${m.separator.name}`);
  return parts.length ? `Heard with ${parts.join(", ")}.` : "No model ran.";
}

/** What the Analyze button needs open on the worker image (the estimate of an analyze item requires all three). */
export const ANALYZE_NEEDS: readonly string[] = ["v2", "ears", "analyze"];

export type AnalyzeGate = { kind: "hidden" } | { kind: "ready" } | { kind: "closed"; reason: string };

/**
 * May the Analyze button be pressed? `caps` are the worker capabilities that are open (get_provider_status, Owner only), or null when
 * the caller cannot read them: the estimate then decides and says why in words.
 */
export function analyzeGate(allowed: readonly string[], caps: readonly string[] | null): AnalyzeGate {
  if (!allowed.includes("estimate_generation")) return { kind: "hidden" };
  if (caps === null) return { kind: "ready" };
  const missing = ANALYZE_NEEDS.filter((n) => !caps.includes(n));
  if (missing.length === 0) return { kind: "ready" };
  return { kind: "closed", reason: `No listening check is possible yet: the worker image is not open for it (missing: ${missing.join(", ")}).` };
}
