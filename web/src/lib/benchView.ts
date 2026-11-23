// Pure helpers for the Critic Bench page (M6N-W1, m6-night C388.1): state chips, the Wilson band geometry and the
// disagreement links. The bench is a read model; nothing here decides trust.
import type { components } from "../api/schema";

export type Bench = components["schemas"]["Bench"];
export type Critic = components["schemas"]["Critic"];
export type Score = components["schemas"]["Score"];
export type Rule = components["schemas"]["Rule"];

const STATE: Record<string, string> = { trusted: "Trusted", contrary: "Contrary", unknown: "Unknown", insufficient: "Not enough pairs" };
export const stateText = (s: string): string => STATE[s] ?? s;

const TIER: Record<string, string> = { A: "Tier A (measured)", B: "Tier B (listening)", agent: "Agent", diction: "Diction" };
export const tierText = (t: string): string => TIER[t] ?? t;

export const percent = (x: number): string => `${Math.round(x * 100)}%`;

const clamp01 = (x: number): number => (Number.isFinite(x) ? Math.min(1, Math.max(0, x)) : 0);
const pct1 = (x: number): number => Math.round(clamp01(x) * 1000) / 10;

export interface Band {
  /** Percent positions along the 0..100 track. */
  lo: number;
  width: number;
  rate: number;
  contraryAt: number;
  trustedAt: number;
}

/** Where the Wilson interval, the observed rate and the two rule lines sit on the bar. */
export function band(s: Pick<Score, "lo" | "hi" | "rate">, rule: Pick<Rule, "contrary_upper" | "trusted_lower">): Band {
  const lo = pct1(s.lo);
  const hi = pct1(s.hi);
  return { lo, width: Math.max(0, Math.round((hi - lo) * 10) / 10), rate: pct1(s.rate), contraryAt: pct1(rule.contrary_upper), trustedAt: pct1(rule.trusted_lower) };
}

export interface ScoreRow {
  key: "ratings" | "duels" | "lines";
  label: string;
  score: Score;
}

const KEYS: [ScoreRow["key"], string][] = [["ratings", "Your stars"], ["duels", "Your duels"], ["lines", "Sung lines"]];

/**
 * The scores a critic actually has pairs for. The wire sends null for a reference the critic is not compared on (a metric
 * critic has no `lines`), though the generated type does not say so, so each is read as nullable.
 */
export function scoreRows(c: Critic): ScoreRow[] {
  const rows: ScoreRow[] = [];
  for (const [key, label] of KEYS) {
    const score = c.scores[key] as Score | null | undefined;
    if (score && score.pairs > 0) rows.push({ key, label, score });
  }
  return rows;
}

/** A duel critic's slot bias is null for every other critic (see scoreRows on the generated type). */
export const isSlotBiased = (c: Critic): boolean => (c.slot_bias as Critic["slot_bias"] | null)?.biased === true;

export function ariaFor(label: string, s: Score): string {
  return `${label}: agrees on ${s.agree} of ${s.pairs} pairs, ${percent(s.rate)} (95% interval ${percent(s.lo)} to ${percent(s.hi)}), ${stateText(s.state)}`;
}

/** A disagreement key is "a|b" (two take ids: Compare) or a duel id (Duels). */
export function disagreementHref(key: string): string {
  const [a, b] = key.split("|");
  return b !== undefined ? `/compare?a=${encodeURIComponent(a ?? "")}&b=${encodeURIComponent(b)}` : `/duels/${encodeURIComponent(key)}`;
}

export function ruleText(r: Rule): string {
  return `Trusted needs at least ${r.min_n} independent pairs and a lower bound above ${percent(r.trusted_lower)}; contrary means the upper bound is below ${percent(r.contrary_upper)}.`;
}
