// Pure helpers for the night-shift pages (M6N-W1, m6-night C388.1): the status headline, run rows, the preview, and the
// secure settings form <-> update_night_shift body.
import type { components } from "../api/schema";
import { formatMicroUSD as usd } from "./money";

export type NightShift = components["schemas"]["NightShift"];
export type NightSettings = components["schemas"]["NightSettings"];
export type NightRun = components["schemas"]["NightRun"];
export type NightPlan = components["schemas"]["NightPlan"];
export type NightPatch = components["schemas"]["UpdateNightShiftInBody"];

/** What the operator must be told plainly (operator decision 2026-11-21). */
export const OFF_NOTICE =
  "The night shift is off until the Owner enables it, with a live mandate. The plan is a budget of $1.00 per night, run at 01:30.";
export const ATTENDED_NOTICE = "Run the first night attended, during the day: set the time a few minutes ahead and watch it from the first take to the last.";
export const HISTORY_NIGHTS = 14;

export interface Status {
  tone: "off" | "idle" | "on" | "halted";
  headline: string;
  detail: string;
}

/** One line of truth about whether the night will spend. */
export function nightStatus(ns: Pick<NightShift, "settings" | "authority">): Status {
  const { settings, authority } = ns;
  if (settings.halted) return { tone: "halted", headline: "Halted", detail: `Halted: ${settings.halted.reason}. Enabling the night shift again in the secure settings clears it.` };
  if (!settings.enabled) return { tone: "off", headline: "Off", detail: "Nothing runs at night until the Owner enables it." };
  if (!authority.mandate) return { tone: "idle", headline: "On, but nothing to spend", detail: authority.reason === "mandate_scheduled" ? "The mandate has not started yet." : "There is no live mandate, so the night spends nothing." };
  return { tone: "on", headline: "On", detail: `Spends only inside the mandate "${authority.mandate.title}", up to ${usd(authority.mandate.budget_micro_usd)}.` };
}

const REASONS: Record<string, string> = {
  over_ceiling: "nothing fit under the ceiling",
  budget: "the budget",
  no_mandate: "no live mandate",
  mandate_scheduled: "the mandate has not started",
  tier_b: "the mandate does not allow listening checks",
  caps: "a spending cap",
  disabled: "the night shift was off",
  halted: "halted",
};
export const reasonLabel = (r: string | null | undefined): string => (r ? (REASONS[r] ?? r.replace(/_/g, " ")) : "");

const RUN_STATE: Record<string, string> = { planned: "Planned", running: "Running", done: "Done", skipped: "Skipped", halted: "Halted", failed: "Failed" };

export interface RunRow {
  night: string;
  state: string;
  stateText: string;
  detail: string;
  batchId: string | null;
  crateId: string | null;
}

export function runRows(runs: NightRun[]): RunRow[] {
  return [...(runs ?? [])]
    .sort((a, b) => (a.night < b.night ? 1 : a.night > b.night ? -1 : 0))
    .slice(0, HISTORY_NIGHTS)
    .map((r) => {
      const why = reasonLabel(r.reason);
      const money = r.takes > 0 ? `${r.takes} take${r.takes === 1 ? "" : "s"}, ceiling ${usd(r.ceiling_micro_usd)}, settled ${usd(r.settled_micro_usd)}` : "";
      const detail = [money, why && (r.state === "done" ? why : `because ${why}`)].filter((x) => x !== "").join("; ");
      return { night: r.night, state: r.state, stateText: RUN_STATE[r.state] ?? r.state, detail, batchId: r.batch_id, crateId: r.morning_crate_id };
    });
}

export interface PlanSummary {
  exploit: number;
  explore: number;
  dropped: number;
  trimmed: number;
  line: string;
}

export function planSummary(p: NightPlan): PlanSummary {
  const items = p.items ?? [];
  const exploit = items.filter((i) => i.kind === "exploit").length;
  const explore = items.filter((i) => i.kind === "explore").length;
  const q = p.quote;
  const price = q ? `expected ${usd(q.expected_micro_usd)}, at most ${usd(q.max_micro_usd)}, ceiling ${usd(q.ceiling_micro_usd)}` : "not priced";
  const head = p.would_skip ? `Tonight would be skipped: ${reasonLabel(p.would_skip)}.` : `${items.length} take${items.length === 1 ? "" : "s"} planned (${exploit} exploit, ${explore} explore); ${price}.`;
  return { exploit, explore, dropped: (p.dropped ?? []).length, trimmed: (p.trimmed ?? []).length, line: head };
}

// --- settings form ---

export interface NightForm {
  at: string;
  maxTakes: string;
  /** Percent of takes that explore, as typed (0 to 50). */
  explorePct: string;
  minStars: string;
  listeningChecks: "on" | "off";
}

/** parts per million -> percent text, exact ("100000" -> "10", "125000" -> "12.5"). */
export function ppmToPercent(ppm: number): string {
  const whole = Math.floor(ppm / 10_000);
  const frac = String(ppm % 10_000).padStart(4, "0").replace(/0+$/, "");
  return frac ? `${whole}.${frac}` : `${whole}`;
}

/** Percent text -> ppm (integer math, up to 4 decimals); null when invalid or above 50%. */
export function percentToPpm(text: string): number | null {
  const m = /^(\d{1,2})(?:\.(\d{1,4}))?$/.exec(text.trim());
  if (!m) return null;
  const ppm = Number(m[1]) * 10_000 + Number((m[2] ?? "").padEnd(4, "0") || "0");
  return ppm <= 500_000 ? ppm : null;
}

export function settingsToForm(s: NightSettings): NightForm {
  return { at: s.at, maxTakes: String(s.max_takes), explorePct: ppmToPercent(s.explore_ppm), minStars: String(s.min_stars), listeningChecks: s.listening_checks };
}

export type PatchResult = { ok: true; body: NightPatch } | { ok: false; error: string };

/** Only changed fields are sent; `enabled` is added when the button asks for it. */
export function formToPatch(f: NightForm, current: NightSettings, enabled?: boolean): PatchResult {
  const body: NightPatch = {};
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(f.at.trim())) return { ok: false, error: "Time: use 24-hour HH:MM, for example 01:30." };
  if (f.at.trim() !== current.at) body.at = f.at.trim();
  if (!/^\d{1,2}$/.test(f.maxTakes.trim()) || Number(f.maxTakes) < 1 || Number(f.maxTakes) > 12) return { ok: false, error: "Takes per night: 1 to 12." };
  if (Number(f.maxTakes) !== current.max_takes) body.max_takes = Number(f.maxTakes);
  const ppm = percentToPpm(f.explorePct);
  if (ppm === null) return { ok: false, error: "Explore share: 0 to 50 percent." };
  if (ppm !== current.explore_ppm) body.explore_ppm = ppm;
  if (!/^[1-5]$/.test(f.minStars.trim())) return { ok: false, error: "Minimum stars: 1 to 5." };
  if (Number(f.minStars) !== current.min_stars) body.min_stars = Number(f.minStars);
  if (f.listeningChecks !== current.listening_checks) body.listening_checks = f.listeningChecks;
  if (enabled !== undefined) body.enabled = enabled;
  if (Object.keys(body).length === 0) return { ok: false, error: "Nothing changed." };
  return { ok: true, body };
}
