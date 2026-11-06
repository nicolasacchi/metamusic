// Which engine actions the take page offers and why one is off (C161): visible only when the engine is adopted and
// the effective cap is on; otherwise disabled with the reason. Pure, from list_engines.
import { ACESTEP } from "./genIntent";
import { capsOf, unavailableText, type EngineList } from "./engines";

export type TrackAction = "repaint" | "reference" | "extend" | "complete" | "stems";

/** The E2b actions: offered only when the engine caps list the task. */
export const TASK_ACTIONS = ["extend", "complete", "stems"] as const;

/** "" when the action is available. */
export function actionReason(list: EngineList, action: TrackAction): string {
  const e = list.engines.find((x) => x.name === ACESTEP);
  if (!e) return "ACE-Step is not available on this server.";
  const why = unavailableText(e);
  if (why) return `ACE-Step: ${why}`;
  const caps = capsOf(list, ACESTEP);
  if (action === "repaint" && !caps?.repaint_interval) return "ACE-Step does not offer repaint here.";
  if (action === "reference" && !caps?.reference_audio) return "ACE-Step does not offer reference renders here.";
  if ((action === "extend" || action === "complete" || action === "stems") && !caps?.tasks?.includes(action)) {
    return `ACE-Step does not offer ${action === "stems" ? "stem extraction" : action} here.`;
  }
  return "";
}

/** Extend: the seconds that may be added to a take of `duration` s, from the caps (the effective cap bounds the total). */
export function extendLimits(list: EngineList | null, duration: number): { min: number; max: number } {
  const c = capsOf(list, ACESTEP);
  if (!c) return { min: 0, max: 0 };
  const room = c.max_seconds > 0 ? Math.floor(c.max_seconds - duration) : Number.POSITIVE_INFINITY;
  return { min: c.extend_min_s, max: Math.min(c.extend_max_s, room) };
}

/** "" when `s` seconds can be added, else why not. */
export function extendProblem(s: number, lim: { min: number; max: number }): string {
  if (!Number.isFinite(s) || s <= 0) return "Enter how many seconds to add.";
  if (lim.max < lim.min) return "The take is already at the longest length this engine renders.";
  if (s < lim.min || s > lim.max) return `Add between ${lim.min} and ${lim.max} s (the total stays within the engine's maximum length).`;
  return "";
}

/** The range limits for an action, 0 meaning none. */
export function actionLimits(list: EngineList | null, action: TrackAction): { min: number; max: number } {
  const c = capsOf(list, ACESTEP);
  if (!c) return { min: 0, max: 0 };
  return action === "repaint" ? { min: c.repaint_min_s, max: c.repaint_max_s } : { min: 0, max: c.reference_max_s };
}

/** Repaint children (operator "repaint") offer the A/B at the repaint range. */
export function isRepaintChild(operator: string | null | undefined, task: string | undefined): boolean {
  return operator === "repaint" || task === "repaint";
}
