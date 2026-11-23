// Pure helpers for the mandate pages (M6N-W1, m6-night C388.1): state chips, usage bars, and the secure create form <->
// create_mandate body. Money is integer micro-USD end to end (architecture rule 6); dollars exist only as typed text.
import type { components } from "../api/schema";
import { parseUSDInput } from "./money";

export type Mandate = components["schemas"]["Mandate"];
export type MandateScope = components["schemas"]["MandateScope"];
export type MandateUsage = components["schemas"]["MandateUsage"];
export type CreateMandateBody = components["schemas"]["CreateMandateInBody"];

/** Limits of C366.2, mirrored for early messages; the server decides. */
export const LIMITS = { maxTitle: 120, maxBudgetMicro: 100_000_000, maxDays: 31, maxTakesPerBatch: 12, maxTakesPerLineage: 50, maxRoots: 200, maxReason: 500 } as const;

export const MODES = [
  { key: "new_seed", label: "New seed (a fresh take of the same song)" },
  { key: "style_edit", label: "Style edit (a variation of a parent take)" },
] as const;
export type ModeKey = (typeof MODES)[number]["key"];

const STATE_LABEL: Record<string, string> = { live: "Live", scheduled: "Scheduled", ended: "Ended", revoked: "Revoked" };
export const stateLabel = (s: string): string => STATE_LABEL[s] ?? s;

/** Only a mandate that can still authorise spending can be revoked. */
export const canRevoke = (m: Pick<Mandate, "effective_state">): boolean => m.effective_state === "live" || m.effective_state === "scheduled";

export interface UsageBar {
  settledPct: number;
  reservedPct: number;
  remainingPct: number;
}

const pct = (n: number, whole: number): number => (whole > 0 ? Math.min(100, Math.max(0, Math.floor((n * 100) / whole))) : 0);

/** Integer percents of the budget; the three never sum past 100. */
export function usageBar(u: Pick<MandateUsage, "settled_micro_usd" | "reserved_micro_usd" | "remaining_micro_usd">, budget: number): UsageBar {
  const settledPct = pct(u.settled_micro_usd, budget);
  const reservedPct = Math.min(100 - settledPct, pct(u.reserved_micro_usd, budget));
  const remainingPct = Math.min(100 - settledPct - reservedPct, pct(u.remaining_micro_usd, budget));
  return { settledPct, reservedPct, remainingPct };
}

/** "Lineage filter" text of a scope for the list. */
export function scopeSummary(s: MandateScope): string {
  const lineages = s.root_track_ids ? `${s.root_track_ids.length} lineage${s.root_track_ids.length === 1 ? "" : "s"}` : "every lineage";
  return `${(s.modes ?? []).join(" + ")}, ${s.min_stars}+ stars, ${lineages}, up to ${s.max_takes_per_batch} takes a night${s.tier_b ? ", listening checks allowed" : ""}`;
}

export function reasonError(reason: string): string | null {
  const t = reason.trim();
  if (t === "") return "A reason is required.";
  if ([...t].length > LIMITS.maxReason) return `The reason is at most ${LIMITS.maxReason} characters.`;
  return null;
}

// --- the secure create form ---

export interface MandateForm {
  title: string;
  /** Dollars as typed. Never prefilled. */
  budget: string;
  /** datetime-local text; blank = now. */
  from: string;
  until: string;
  /** Per-night ceiling, dollars as typed. Never prefilled. */
  ceiling: string;
  takesPerNight: string;
  takesPerLineage: string;
  modes: Record<ModeKey, boolean>;
  minStars: string;
  /** Root track ids, one per line or comma separated; blank = every lineage. */
  roots: string;
  tierB: boolean;
}

/** The empty form: no money field is filled in, so the Owner types every figure. */
export function emptyMandateForm(): MandateForm {
  return { title: "", budget: "", from: "", until: "", ceiling: "", takesPerNight: "3", takesPerLineage: "1", modes: { new_seed: true, style_edit: false }, minStars: "4", roots: "", tierB: false };
}

export type FormResult = { ok: true; body: CreateMandateBody } | { ok: false; error: string };

const fail = (error: string): FormResult => ({ ok: false, error });

function whole(text: string, lo: number, hi: number): number | null {
  const t = text.trim();
  if (!/^\d{1,4}$/.test(t)) return null;
  const n = Number(t);
  return n >= lo && n <= hi ? n : null;
}

function when(text: string): Date | null {
  const d = new Date(text.trim());
  return Number.isNaN(d.getTime()) ? null : d;
}

export function parseRoots(text: string): string[] {
  return text.split(/[\s,]+/).filter((x) => x !== "");
}

/** Validates the form (the server still decides) and builds the create_mandate body; the holder is always the night shift. */
export function formToMandate(f: MandateForm, now: Date): FormResult {
  const title = f.title.trim();
  if (title === "" || [...title].length > LIMITS.maxTitle) return fail(`Give the mandate a title of 1 to ${LIMITS.maxTitle} characters.`);
  const budget = parseUSDInput(f.budget);
  if (budget === null || budget < 1 || budget > LIMITS.maxBudgetMicro) return fail(`Budget: type dollars (at most 6 decimals), up to $${LIMITS.maxBudgetMicro / 1_000_000}.`);
  const ceiling = parseUSDInput(f.ceiling);
  if (ceiling === null || ceiling < 1) return fail("Per-night ceiling: type dollars (at most 6 decimals).");
  if (ceiling > budget) return fail("The per-night ceiling cannot be above the budget.");
  const from = f.from.trim() === "" ? now : when(f.from);
  if (!from) return fail("Starts: that is not a date and time.");
  const until = when(f.until);
  if (!until) return fail("Ends: pick the date and time the mandate stops.");
  if (until.getTime() <= from.getTime()) return fail("The mandate must end after it starts.");
  if (until.getTime() - from.getTime() > LIMITS.maxDays * 86_400_000) return fail(`A mandate lasts at most ${LIMITS.maxDays} days.`);
  const perNight = whole(f.takesPerNight, 1, LIMITS.maxTakesPerBatch);
  if (perNight === null) return fail(`Takes per night: 1 to ${LIMITS.maxTakesPerBatch}.`);
  const perLineage = whole(f.takesPerLineage, 1, LIMITS.maxTakesPerLineage);
  if (perLineage === null) return fail(`Takes per lineage: 1 to ${LIMITS.maxTakesPerLineage}.`);
  const stars = whole(f.minStars, 1, 5);
  if (stars === null) return fail("Minimum stars: 1 to 5.");
  const modes = MODES.filter((m) => f.modes[m.key]).map((m) => m.key);
  if (modes.length === 0) return fail("Allow at least one mode.");
  const roots = parseRoots(f.roots);
  if (new Set(roots).size !== roots.length) return fail("The lineage filter names a lineage twice.");
  if (roots.length > LIMITS.maxRoots) return fail(`The lineage filter holds at most ${LIMITS.maxRoots} lineages.`);
  const body: CreateMandateBody = {
    holder: "night_shift",
    title,
    budget_micro_usd: budget,
    valid_until: until.toISOString(),
    scope: {
      modes,
      engines: ["yue2"],
      min_stars: stars,
      root_track_ids: roots.length > 0 ? roots : null,
      max_takes_per_lineage: perLineage,
      max_takes_per_batch: perNight,
      max_batch_ceiling_micro_usd: ceiling,
      max_batches_per_day: 1, // the night makes one batch
      tier_b: f.tierB,
    },
  };
  if (f.from.trim() !== "") body.valid_from = from.toISOString();
  return { ok: true, body };
}

/** "2026-11-22 10:00 UTC" from an RFC 3339 instant; the raw text when it is not one. */
export function whenText(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toISOString().slice(0, 16).replace("T", " ") + " UTC";
}
