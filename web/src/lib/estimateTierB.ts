// The listening check (Tier B) in the estimate and confirm UI (M5b C347.2): a badge per take, the skip option, the cost in words
// and readable refusals. Pure; EstimateView, ConfirmSpend and the generate page call it.
import type { components } from "../api/schema";
import { formatMicroUSD, formatMicroUSDCeil } from "./money";

type TakePlan = components["schemas"]["TakePlan"];
type Warning = components["schemas"]["EstimateWarning"];
type EstimateShape = {
  engine: string;
  items: { mode: string; takes: TakePlan[] }[];
  cost: { expected_micro_usd: number; ceiling_micro_usd: number };
};

const WARNING_TEXT: Record<string, string> = {
  tier_b_unavailable: "No listening check: the worker image is not open for it yet.",
  tier_b_no_lyrics: "No listening check: the lyrics have no line to listen for.",
  tier_b_exceeds_runtime: "No listening check: it would not fit the runtime cap.",
  tier_b_engine: "No listening check: it is available for yue2 renders only.",
};

/** Plain words for a tier_b_* warning code; "" for any other code. */
export function tierBWarningText(code: string): string {
  return WARNING_TEXT[code] ?? "";
}

export interface TakeBadge {
  on: boolean;
  label: "listening check" | "no listening check";
  /** Why a take has none, from its own tier_b warning; "" when none was given (the option was skipped). */
  reason: string;
}

export function takeBadge(t: TakePlan, itemMode: string, warnings: readonly Warning[]): TakeBadge {
  if (itemMode === "analyze" || t.operator === "analyze" || t.tier_b === true) return { on: true, label: "listening check", reason: "" };
  const w = warnings.find((x) => x.take_index === t.take_index && tierBWarningText(x.code) !== "");
  return { on: false, label: "no listening check", reason: w ? tierBWarningText(w.code) : "" };
}

export function isAnalyzeEstimate(e: Pick<EstimateShape, "items">): boolean {
  return e.items.length > 0 && e.items.every((it) => it.mode === "analyze");
}

export function tierBSummary(e: Pick<EstimateShape, "items">): { analyze: boolean; checked: number; total: number } {
  const analyze = isAnalyzeEstimate(e);
  const takes = e.items.flatMap((it) => it.takes);
  return { analyze, checked: analyze ? takes.length : takes.filter((t) => t.tier_b === true).length, total: takes.length };
}

/** The "Skip the listening check" option belongs to yue2 renders (an analyze estimate takes no option, another engine has no check). */
export function hasListeningCheckChoice(e: Pick<EstimateShape, "items" | "engine">): boolean {
  return e.engine === "yue2" && !isAnalyzeEstimate(e) && e.items.some((it) => it.takes.length > 0);
}

/**
 * What the check costs, in words: the whole estimate for an analyze, else the difference to the same estimate with tier_b false
 * (`without`). "" when it cannot be said (no comparison, or no take has the check).
 */
export function checkCostLine(e: EstimateShape, without: Pick<EstimateShape, "cost"> | null): string {
  if (isAnalyzeEstimate(e)) return `The listening check is the whole job: up to ${formatMicroUSDCeil(e.cost.ceiling_micro_usd)}.`;
  if (!without || tierBSummary(e).checked === 0) return "";
  const ceiling = e.cost.ceiling_micro_usd - without.cost.ceiling_micro_usd;
  const expected = e.cost.expected_micro_usd - without.cost.expected_micro_usd;
  if (ceiling <= 0) return "";
  return `The listening check adds up to ${formatMicroUSDCeil(ceiling)} to the ceiling (about ${formatMicroUSD(Math.max(0, expected))} expected).`;
}

/** The text of a refused estimate: the Tier B refusals in words, anything else as "CODE: message". */
export function estimateFailureText(status: number, code: string, message: string, details: Record<string, unknown> | undefined): string {
  const d = details ?? {};
  if (status === 503 && d.reason === "worker_outdated") {
    const missing = Array.isArray(d.missing) ? d.missing.filter((x): x is string => typeof x === "string") : [];
    return `The worker image is not open for this yet${missing.length ? ` (missing: ${missing.join(", ")})` : ""}: nothing can be priced until the operator opens it.`;
  }
  if (code === "ANALYSIS_IN_PROGRESS") return "This take is already being checked: wait for that check to finish.";
  if (code === "INVALID_ITEM") {
    if (d.rule === "already_analyzed") return "This take already has a listening check.";
    if (d.rule === "audio_too_long") return "This take is too long for a listening check (at most 10 minutes).";
    if (d.rule === "tier_b_exceeds_runtime") return "The listening check would not fit the runtime cap for this take.";
  }
  if (code && message) return `${code}: ${message}`;
  return "Could not estimate.";
}
