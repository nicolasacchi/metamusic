// Confirming a spend (C84): one idempotency key per click, kept across retries; failure rendering.
import type { createApi } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { components } from "../api/schema";
import { formatMicroUSD, formatMicroUSDCeil } from "./money";

type Api = ReturnType<typeof createApi>;
type Estimate = components["schemas"]["Estimate"];

export type ConfirmOutcome =
  | { kind: "created"; batchId: string }
  /** 409 CONFIRMATION_INVALID naming the batch the token already created. */
  | { kind: "existing"; batchId: string }
  | { kind: "network" }
  | { kind: "failed"; status: number; code: string; message: string; details: Record<string, unknown> };

export function confirmButtonLabel(e: Estimate): string {
  return `Spend up to ${formatMicroUSDCeil(e.cost.ceiling_micro_usd)}`;
}

/**
 * One user click: the returned function may be called again to retry after a network error and
 * always sends the same Idempotency-Key. A new click makes a new action (a fresh key).
 */
export function createConfirmAction(api: Api, token: string, rationale: string): () => Promise<ConfirmOutcome> {
  return idempotentAction(async (key): Promise<ConfirmOutcome> => {
    try {
      const r = await api.POST("/human/api/generations", {
        params: { header: { "Idempotency-Key": key } },
        body: { confirmation_token: token, ...(rationale.trim() ? { rationale: rationale.trim() } : {}) },
      });
      if (r.data) return { kind: "created", batchId: r.data.id };
      const status = r.response.status;
      const details = (r.error?.details ?? {}) as Record<string, unknown>;
      if (status === 409 && r.error?.code === "CONFIRMATION_INVALID" && typeof details.batch_id === "string" && details.batch_id) {
        return { kind: "existing", batchId: details.batch_id };
      }
      return { kind: "failed", status, code: r.error?.code ?? "", message: r.error?.message ?? "", details };
    } catch {
      return { kind: "network" };
    }
  });
}

export interface FailureView {
  text: string;
  /** Offer "Re-estimate" (the token is spent or expired). */
  reestimate: boolean;
}

function micro(v: unknown): string | null {
  return typeof v === "number" && Number.isFinite(v) ? formatMicroUSD(v) : null;
}

export function describeConfirmFailure(f: Extract<ConfirmOutcome, { kind: "failed" }>): FailureView {
  const d = f.details;
  if (f.status === 402) {
    const parts = [`Over budget: the ${typeof d.cap === "string" ? d.cap : "spend"} cap would be exceeded.`];
    const limit = micro(d.limit);
    const used = micro(d.used);
    const req = typeof d.requested === "number" && Number.isFinite(d.requested) ? formatMicroUSDCeil(d.requested) : null;
    if (limit) parts.push(`Cap ${limit}.`);
    if (used) parts.push(`Used ${used}.`);
    if (req) parts.push(`This batch needs up to ${req}.`);
    return { text: parts.join(" "), reestimate: false };
  }
  if (f.status === 410) return { text: "The estimate expired. Re-estimate to get a fresh confirmation.", reestimate: true };
  if (f.status === 409) return { text: "This estimate was already used or is not yours. Re-estimate to continue.", reestimate: true };
  if (f.status === 503) {
    const reason = typeof d.reason === "string" ? ` (${d.reason})` : "";
    return { text: `${f.code || "GENERATION_DISABLED"}${reason}: generation is not available right now.`, reestimate: false };
  }
  return { text: f.message || f.code || `Request failed (${f.status})`, reestimate: false };
}

/** Why Confirm must be disabled for this estimate; empty when it may be pressed (C84, M3-W1 Done). */
export function confirmDisabledReasons(e: Estimate): string[] {
  const out: string[] = [];
  const errs = e.warnings.filter((w) => w.severity === "error").length;
  if (errs > 0) out.push(`${errs} error${errs === 1 ? "" : "s"} in the plan must be fixed first.`);
  if (!e.provider.generation_enabled) out.push(`Generation is disabled${e.provider.disabled_reason ? ` (${e.provider.disabled_reason})` : ""}.`);
  if (!e.provider.armed) out.push("The provider is disarmed.");
  if (e.provider.tripped) out.push("The provider is tripped.");
  if (!e.budget.fits) out.push(`Over budget: ${e.budget.would_exceed.length ? e.budget.would_exceed.join(", ") + " cap" : "cap"} would be exceeded.`);
  return out;
}
