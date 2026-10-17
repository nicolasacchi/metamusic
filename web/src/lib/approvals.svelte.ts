// Shared state for the pending-approvals badge (App) and section (Jobs): one list_batches call, one whoami call.
import { api } from "../api/client";
import { loadPendingApprovals, type PendingBatch } from "./approvals";

export const approvals = $state<{ items: PendingBatch[]; more: boolean; loaded: boolean; viewer: "human" | "agent" | "device" | null; stepUpUntil: string | null }>({
  items: [],
  more: false,
  loaded: false,
  viewer: null,
  stepUpUntil: null,
});

let viewerAsked = false;

async function loadViewer() {
  if (viewerAsked) return;
  viewerAsked = true;
  try {
    const r = await api.GET("/api/me", {});
    if (r.data) {
      approvals.viewer = r.data.kind;
      approvals.stepUpUntil = r.data.step_up_until;
    } else viewerAsked = false;
  } catch {
    viewerAsked = false;
  }
}

export async function refreshApprovals(): Promise<void> {
  void loadViewer();
  const p = await loadPendingApprovals({ GET: (path, init) => api.GET(path, init) });
  if (!p) return;
  approvals.items = p.items;
  approvals.more = p.more;
  approvals.loaded = true;
}

// A confirmation just completed in the other tab: the deadline moved.
if (typeof window !== "undefined") {
  window.addEventListener("mm:stepup-done", () => {
    viewerAsked = false;
    void loadViewer();
  });
}
