// Pure model of rendering from the Desk (DESK-W2, m5b-desk C363.2): the render_plan and plan_seeds intents, who may ask for
// them, what is open on the worker, and how a plan's render jobs read. Both modes go through the ordinary estimate and
// ConfirmSpend flow on /human/generate: nothing here creates a job, and nothing costs money until the Owner approves.
import { estimateFailureText } from "./estimateTierB";
import { DESK_PLAN_SEEDS_MAX, DESK_RENDER_SEEDS_MAX, type GenerationIntent } from "./genIntent";
import type { PlanRender } from "./deskApi";
import type { TransformRequest } from "./transformScore";

export const SEEDS_DEFAULT = 8;
export const SEEDS_MAX = DESK_PLAN_SEEDS_MAX;
export const RENDER_SEEDS_MAX = DESK_RENDER_SEEDS_MAX;
/** The worker capabilities a plan job needs (jobspec.Needs for kind plan). */
export const PLAN_NEEDS: readonly string[] = ["plan", "v2"];

/** "Render" on a plan: the plan's score through YuE2, `n` seeds of it (1..4; more than one is "same score, new performance"). */
export function renderIntent(planIds: readonly string[], nSeeds = 1): GenerationIntent {
  return { mode: "render_plan", ...(nSeeds > 1 ? { n_seeds: nSeeds } : {}), desk: { plan_ids: [...planIds] } };
}

export interface SeedsForm {
  base: string;
  seeds: string;
  /** "" = the take's own cot. */
  cot: "" | "full" | "melody";
}
export const defaultSeedsForm = (base = ""): SeedsForm => ({ base, seeds: String(SEEDS_DEFAULT), cot: "" });

export interface BuiltSeeds {
  intent: GenerationIntent | null;
  errors: Record<string, string>;
}
/** The plan_seeds intent of the /desk form; a bad field is a per-field message and no intent. */
export function buildPlanSeeds(f: SeedsForm): BuiltSeeds {
  const errors: Record<string, string> = {};
  const base = f.base.trim();
  if (base === "") errors.base = "Choose the take to plan scores for";
  const t = f.seeds.trim();
  const n = Number(t);
  if (t === "" || !Number.isFinite(n)) errors.seeds = "Enter a number of seeds";
  else if (!Number.isInteger(n) || n < 1 || n > SEEDS_MAX) errors.seeds = `Seeds must be a whole number from 1 to ${SEEDS_MAX}`;
  if (Object.keys(errors).length > 0) return { intent: null, errors };
  return { intent: { mode: "plan_seeds", parent_track_id: base, desk: { n_seeds: n, ...(f.cot ? { cot: f.cot } : {}) } }, errors };
}

export type Gate = { kind: "hidden" } | { kind: "ready" } | { kind: "closed"; reason: string };

const mayEstimate = (allowed: readonly string[]): boolean =>
  allowed.includes("estimate_generation") || allowed.includes("create_generation_human") || allowed.includes("create_generation");

/**
 * plan_seeds needs the worker image open for plan and v2 (G1's capability gate). The open set is the Owner's to read
 * (get_provider_status); a principal that cannot read it learns from the estimate, in words (planSeedsRefusalText). A closed
 * gate is said up front so nothing fails after a click.
 */
export function planSeedsGate(allowed: readonly string[], caps: readonly string[] | null): Gate {
  if (!mayEstimate(allowed)) return { kind: "hidden" };
  if (caps === null) return { kind: "ready" };
  const missing = PLAN_NEEDS.filter((n) => !caps.includes(n));
  if (missing.length === 0) return { kind: "ready" };
  return {
    kind: "closed",
    reason: `Planning new scores is not available yet: the worker image is not open for it (missing: ${missing.join(", ")}). Plans from a take's own score and edits still work, and cost nothing.`,
  };
}

/** render_plan is a plain generate job on the first worker protocol: it needs no capability, only the right to estimate. */
export function renderGate(allowed: readonly string[]): Gate {
  return mayEstimate(allowed) ? { kind: "ready" } : { kind: "hidden" };
}

/** A refused plan_seeds estimate or create, in words (a closed capability says so plainly). */
export function planSeedsRefusalText(status: number, code: string, message: string, details: Record<string, unknown> | undefined): string {
  if (status === 503 && details?.reason === "worker_outdated") {
    const missing = Array.isArray(details.missing) ? details.missing.filter((x): x is string => typeof x === "string") : [];
    return `Planning new scores is not available yet: the worker image is not open for it${missing.length ? ` (missing: ${missing.join(", ")})` : ""}.`;
  }
  return estimateFailureText(status, code, message, details);
}

const STATE_TEXT: Record<string, string> = {
  completed: "done",
  awaiting_confirmation: "waiting for the Owner",
  queued: "queued",
  running: "rendering",
  failed: "failed",
  cancelled: "cancelled",
  expired: "expired",
};
export const renderStateText = (s: string): string => STATE_TEXT[s] ?? s;

export interface RenderRow {
  key: string;
  text: string;
  trackHref: string | null;
  batchHref: string;
}
/** The render jobs of a plan, one row each. */
export function renderRows(renders: readonly PlanRender[] | undefined): RenderRow[] {
  return (renders ?? []).map((r) => ({
    key: r.job_id,
    text: `seed ${r.seed} · ${renderStateText(r.state)}`,
    trackHref: r.track_id ? `/tracks/${encodeURIComponent(r.track_id)}` : null,
    batchHref: `/batches/${encodeURIComponent(r.batch_id)}`,
  }));
}

/** A plan job's batch links here: the plans that job made. */
export const planJobsHref = (jobId: string): string => `/desk?job_id=${encodeURIComponent(jobId)}`;

/**
 * "Keep as Take": an explicit score_render transform of the plan's own score text (CPU, no money). It is the only way a plan
 * becomes a Take without a GPU render. Null when the plan carries no score text.
 */
export function keepRequest(p: { abc?: string; title?: string; base: { title: string }; score: { dialect: "yue2" | "standard" } }): TransformRequest | null {
  if (!p.abc || p.abc.trim() === "") return null;
  const name = (p.title || p.base.title || "").trim().slice(0, 200);
  return {
    kind: "score_render",
    title: name === "" ? null : name,
    score_render: { score: { abc: p.abc }, dialect: p.score.dialect, soundfont: null, bpm: null, tail_s: null, gain: null, chords: null },
  } as TransformRequest;
}
