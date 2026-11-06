// Callback Listening view helpers (C255, C262): pure text and state mapping for the Judge Callbacks sub-view.
// The components own the network; every answer a 501/503 stub or a missing service can give is mapped here.
import type { createApi } from "../api/client";
import { fmt } from "./time";
import { listCallbacks, VERDICTS, type Callback, type CallbackVerdict } from "./callbacksApi";
import type { Failure } from "./crateApi";

type Api = ReturnType<typeof createApi>;
type Verdict = CallbackVerdict["verdict"];

/** 501 NOT_IMPLEMENTED (lane not merged) and 503 UNAVAILABLE (service not wired) both read as "not available here". */
export const isUnavailable = (f: Pick<Failure, "status">): boolean => f.status === 501 || f.status === 503;

export const UNAVAILABLE_TEXT = "Callbacks are not available on this server yet.";

const LABELS: Record<Verdict, string> = { fixed: "Fixed", not_yet: "Not yet", worse: "Worse", kept: "Kept", lost: "Lost" };

/** The big answer buttons of a callback, in order (fix: fixed / not yet / worse; keep: kept / lost). */
export function answerButtons(kind: Callback["kind"]): { verdict: Verdict; label: string }[] {
  return VERDICTS[kind].map((verdict) => ({ verdict, label: LABELS[verdict] }));
}

export const verdictLabel = (v: Verdict): string => LABELS[v];

/** What the listener is asked about the spot. */
export function question(cb: Pick<Callback, "kind">): string {
  return cb.kind === "fix" ? "Did the patch fix it?" : "Is it still there?";
}

/** "Chorus 2 · 1:12", or "1:12 · by proportion" when no timed section anchors the spot. */
export function spotText(cb: Pick<Callback, "spot">): string {
  const { label, child_at_s, basis } = cb.spot;
  const t = fmt(child_at_s);
  if (label) return `${label} · ${t}`;
  return basis === "proportional" ? `${t} · by proportion` : t;
}

/** A low-confidence spot is flagged so the listener knows to look around it. */
export const roughSpot = (cb: Pick<Callback, "spot">): boolean => cb.spot.confidence < 0.5;

export const waitingText = (n: number): string => (n === 1 ? "1 callback waiting" : `${n} callbacks waiting`);

/** The text after an answer: replayed answers say so; a lost keep names the regression comment it opened. */
export function answeredText(r: { replayed: boolean; verdict: { verdict: Verdict }; regression_comment_id: string | null }): string {
  const base = `Answered: ${verdictLabel(r.verdict.verdict).toLowerCase()}`;
  const extra = r.regression_comment_id ? ". A fix note was added to the new take." : "";
  return (r.replayed ? `${base} (already recorded)` : base) + extra;
}

export type Waiting = { kind: "count"; n: number } | { kind: "unavailable" } | { kind: "error" };

/** The Judge landing count: pending callbacks from list_callbacks (limit 1: only the total is read). */
export async function loadWaiting(api: Api): Promise<Waiting> {
  try {
    const r = await listCallbacks(api, { state: ["pending"], limit: 1 });
    if (r.ok) return { kind: "count", n: r.value.total };
    return isUnavailable(r) ? { kind: "unavailable" } : { kind: "error" };
  } catch {
    return { kind: "error" };
  }
}

/** The pending callback after `id` in list order (wrapping to the first other one), or null when none remain. */
export function nextPending(items: readonly Pick<Callback, "id" | "state">[], id: string): string | null {
  const pending = items.filter((c) => c.state === "pending" && c.id !== id);
  const i = items.findIndex((c) => c.id === id);
  return (pending.find((c) => items.indexOf(c) > i) ?? pending[0])?.id ?? null;
}
