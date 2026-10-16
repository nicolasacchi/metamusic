// Pending spend approvals: batches in awaiting_confirmation with a pending approval. Pure helpers plus the one
// list_batches call behind the nav badge, the Jobs section and the Batch page link.
import type { components } from "../api/schema";

export type PendingBatch = components["schemas"]["BatchSummary"];
export type BatchPage = components["schemas"]["BatchPage"];

/** One page is plenty for a single-user lab; a longer queue shows as "50+". */
export const APPROVALS_LIMIT = 50;

export interface ApprovalsClient {
  GET(
    path: "/api/batches",
    init: { params: { query: { state: "awaiting_confirmation"[]; limit: number } } },
  ): Promise<{ data?: Pick<BatchPage, "items" | "next_cursor">; error?: unknown }>;
}

export interface Pending {
  items: PendingBatch[];
  more: boolean;
}

export function isPending(b: Pick<PendingBatch, "state" | "approval">): boolean {
  return b.state === "awaiting_confirmation" && b.approval.state === "pending";
}

export async function loadPendingApprovals(client: ApprovalsClient): Promise<Pending | null> {
  try {
    const r = await client.GET("/api/batches", { params: { query: { state: ["awaiting_confirmation"], limit: APPROVALS_LIMIT } } });
    if (!r.data) return null;
    return { items: r.data.items.filter(isPending), more: r.data.next_cursor !== null };
  } catch {
    return null;
  }
}

/** The nav badge text; null (no badge) when nothing waits. */
export function badgeText(count: number, more: boolean): string | null {
  if (count <= 0) return null;
  return more ? `${count}+` : String(count);
}

/** Where the operator decides. /human/* is a full page load so the server's 2FA / step-up check runs first. */
export function confirmHref(id: string): string {
  return `/human/confirm/${encodeURIComponent(id)}`;
}

const REASONS: Record<string, string> = {
  takes_over_limit: "more takes than the auto-approve limit",
  cost_over_limit: "cost above the auto-approve limit",
};

export function reasonLabel(code: string): string {
  return REASONS[code] ?? code.replaceAll("_", " ");
}

function span(ms: number): string {
  const m = Math.floor(ms / 60_000);
  if (m < 1) return "under a minute";
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} h`;
  return `${Math.floor(h / 24)} d`;
}

export function ageLabel(createdAt: string, nowMs: number): string {
  const t = Date.parse(createdAt);
  if (Number.isNaN(t)) return "";
  const d = nowMs - t;
  return d < 60_000 ? "just now" : `${span(d)} ago`;
}

export function expiryLabel(expiresAt: string, nowMs: number): string {
  const t = Date.parse(expiresAt);
  if (Number.isNaN(t)) return "";
  return t <= nowMs ? "expired" : `expires in ${span(t - nowMs)}`;
}

export const DECISIONS_SHOWN = 10;

export interface DecisionsClient {
  GET(path: "/api/batches", init: { params: { query: { limit: number } } }): Promise<{ data?: Pick<BatchPage, "items">; error?: unknown }>;
}

/** Batches an approval was decided on (approved, rejected, expired; never auto), newest decision first. */
export function recentDecisions(items: readonly PendingBatch[], n: number = DECISIONS_SHOWN): PendingBatch[] {
  const at = (b: PendingBatch) => Date.parse(b.approval.decided_at ?? b.created_at) || 0;
  return items
    .filter((b) => b.approval.state === "approved" || b.approval.state === "rejected" || b.approval.state === "expired")
    .sort((a, b) => at(b) - at(a))
    .slice(0, n);
}

/** list_batches has no approval filter: the newest page is filtered here. */
export async function loadRecentDecisions(client: DecisionsClient): Promise<PendingBatch[] | null> {
  try {
    const r = await client.GET("/api/batches", { params: { query: { limit: APPROVALS_LIMIT } } });
    return r.data ? recentDecisions(r.data.items) : null;
  } catch {
    return null;
  }
}
