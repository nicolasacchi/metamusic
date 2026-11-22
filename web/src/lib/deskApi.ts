// Typed wrappers over the six Desk operations (DESK-W1, m5b-desk C363.2): list_plans, get_plan, check_abc,
// create_plan_from_take, edit_plan and set_plan_verdict. Network only through the generated client; every function
// answers a value or a Failure (with the problem's details, which the bar editor reads), never throws.
import type { createApi } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { components, operations } from "../api/schema";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];
export type Plan = S["Plan"];
export type PlanSummary = S["PlanSummary"];
export type PlanBarSpan = S["PlanBarSpan"];
export type ScoreEditOp = S["ScoreEditOp"];
export type ScoreReport = S["ScoreReport"];
export type PlanQuery = NonNullable<operations["list_plans"]["parameters"]["query"]>;

export interface DeskFailure {
  ok: false;
  status: number;
  code: string;
  message: string;
  details: Record<string, unknown>;
}
export type Res<T> = { ok: true; value: T } | DeskFailure;

export function failure(status: number, e: unknown, fallback: string): DeskFailure {
  const o = typeof e === "object" && e !== null ? (e as { code?: unknown; message?: unknown; details?: unknown }) : {};
  const details = typeof o.details === "object" && o.details !== null && !Array.isArray(o.details) ? (o.details as Record<string, unknown>) : {};
  return { ok: false, status, code: typeof o.code === "string" ? o.code : "", message: (typeof o.message === "string" && o.message) || fallback, details };
}

async function guard<T>(fallback: string, run: () => Promise<Res<T>>): Promise<Res<T>> {
  try {
    return await run();
  } catch {
    return { ok: false, status: 0, code: "NETWORK", message: `${fallback}: network error`, details: {} };
  }
}
const fail = (r: { response: { status: number }; error?: unknown }, fallback: string) => failure(r.response.status, r.error, fallback);

export function listPlans(api: Api, query: PlanQuery = {}): Promise<Res<{ items: PlanSummary[]; next: string | null }>> {
  return guard("Could not load the plans", async () => {
    const r = await api.GET("/api/plans", { params: { query } });
    return r.data ? { ok: true, value: { items: r.data.items ?? [], next: r.data.next_cursor } } : fail(r, "Could not load the plans");
  });
}

/** The page's read: the score text, the bar spans (for clicking and editing) and the display rewrite; no auditions or renders yet (DESK-W2). */
export const PLAN_INCLUDE = "abc,bars,display";
export function getPlan(api: Api, id: string, include: string = PLAN_INCLUDE): Promise<Res<Plan>> {
  return guard("Could not load the plan", async () => {
    const r = await api.GET("/api/plans/{id}", { params: { path: { id }, query: { include } } });
    if (r.data) return { ok: true, value: r.data };
    return r.response.status === 404 ? { ok: false, status: 404, code: "NOT_FOUND", message: "Plan not found", details: {} } : fail(r, "Could not load the plan");
  });
}

export function checkAbc(api: Api, abc: string, baseAbc?: string): Promise<Res<ScoreReport>> {
  return guard("Could not check the score", async () => {
    const r = await api.POST("/api/abc/check", { body: { abc, ...(baseAbc !== undefined ? { base_abc: baseAbc } : {}), dialect: "yue2" } });
    return r.data ? { ok: true, value: r.data } : fail(r, "Could not check the score");
  });
}

/** create_plan_from_take. One user action: a retry reuses the Idempotency-Key. created is false when the take already had its plan. */
export function openTakeInDesk(api: Api, trackId: string): () => Promise<Res<{ plan: Plan; created: boolean }>> {
  const send = idempotentAction((key) => api.POST("/api/tracks/{id}/plan", { params: { path: { id: trackId }, header: { "Idempotency-Key": key } } }));
  return () =>
    guard("Could not open the take in the Desk", async () => {
      const r = await send();
      return r.data ? { ok: true, value: { plan: r.data, created: r.response.status === 201 } } : fail(r, "Could not open the take in the Desk");
    });
}

/** edit_plan: the edited plan never changes; a new plan comes back (201), or the existing one for the same result (200). */
export function editPlan(api: Api, planId: string, ops: ScoreEditOp[], rationale?: string): () => Promise<Res<{ plan: Plan; created: boolean }>> {
  const send = idempotentAction((key) =>
    api.POST("/api/plans/{id}/edits", { params: { path: { id: planId }, header: { "Idempotency-Key": key } }, body: { ops, ...(rationale ? { rationale } : {}) } }),
  );
  return () =>
    guard("Could not edit the plan", async () => {
      const r = await send();
      return r.data ? { ok: true, value: { plan: r.data, created: r.response.status === 201 } } : fail(r, "Could not edit the plan");
    });
}

export function setVerdict(api: Api, planId: string, verdict: "liked" | "rejected" | null, note?: string): Promise<Res<Plan>> {
  return guard("Could not save the verdict", async () => {
    const r = await api.PUT("/api/plans/{id}/verdict", { params: { path: { id: planId } }, body: { verdict, ...(note ? { note } : {}) } });
    return r.data ? { ok: true, value: r.data } : fail(r, "Could not save the verdict");
  });
}
