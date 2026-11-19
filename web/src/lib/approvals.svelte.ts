// Shared state for the pending-approvals badge (App) and section (Jobs): one list_batches call. The caller comes from the
// `me` store (one whoami call for the whole app).
import { api } from "../api/client";
import { loadPendingApprovals, type PendingBatch } from "./approvals";
import { can, me } from "./me.svelte";

export const approvals = $state<{ items: PendingBatch[]; more: boolean; loaded: boolean }>({
  items: [],
  more: false,
  loaded: false,
});

/** Who polls: the Owner and principals with no role (agents, devices). Editors see their own batches on the Jobs page instead. */
export function pollsApprovals(): boolean {
  return can("list_batches") && (me.role === null || me.role === "owner");
}

/** The badge and the Approvals row are for whoever may decide a batch. */
export function showsApprovalBadge(): boolean {
  return me.kind === "human" && can("approve_batch");
}

export async function refreshApprovals(): Promise<void> {
  if (!pollsApprovals()) return;
  const p = await loadPendingApprovals({ GET: (path, init) => api.GET(path, init) });
  if (!p) return;
  approvals.items = p.items;
  approvals.more = p.more;
  approvals.loaded = true;
}
