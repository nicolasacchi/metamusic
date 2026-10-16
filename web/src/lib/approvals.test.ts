import { describe, expect, test } from "bun:test";
import { ageLabel, badgeText, confirmHref, expiryLabel, isPending, loadPendingApprovals, reasonLabel, type ApprovalsClient, type PendingBatch } from "./approvals";

const mk = (state: PendingBatch["state"], approval: PendingBatch["approval"]["state"]) => ({ state, approval: { state: approval } }) as unknown as PendingBatch;

describe("approvals", () => {
  test("calls list_batches(state=awaiting_confirmation) and keeps only pending approvals", async () => {
    const calls: unknown[] = [];
    const client: ApprovalsClient = {
      GET: (path, init) => {
        calls.push([path, init.params.query]);
        return Promise.resolve({ data: { items: [mk("awaiting_confirmation", "pending"), mk("awaiting_confirmation", "expired")], next_cursor: null } });
      },
    };
    const p = await loadPendingApprovals(client);
    expect(p?.items).toHaveLength(1);
    expect(p?.more).toBe(false);
    expect(calls).toEqual([["/api/batches", { state: ["awaiting_confirmation"], limit: 50 }]]);
  });
  test("a next cursor means more", async () => {
    const p = await loadPendingApprovals({ GET: () => Promise.resolve({ data: { items: [], next_cursor: "c" } }) });
    expect(p?.more).toBe(true);
  });
  test("failures yield null, never a stale zero", async () => {
    expect(await loadPendingApprovals({ GET: () => Promise.reject(new Error("x")) })).toBeNull();
    expect(await loadPendingApprovals({ GET: () => Promise.resolve({ error: {} }) })).toBeNull();
  });
  test("isPending needs both the batch state and the approval state", () => {
    expect(isPending(mk("awaiting_confirmation", "pending"))).toBe(true);
    expect(isPending(mk("active", "pending"))).toBe(false);
    expect(isPending(mk("awaiting_confirmation", "approved"))).toBe(false);
  });
  test("badgeText", () => {
    expect(badgeText(0, false)).toBeNull();
    expect(badgeText(3, false)).toBe("3");
    expect(badgeText(50, true)).toBe("50+");
  });
  test("confirmHref targets the human confirm page", () => {
    expect(confirmHref("abc")).toBe("/human/confirm/abc");
  });
  test("reasonLabel knows the codes and degrades for unknown ones", () => {
    expect(reasonLabel("takes_over_limit")).toContain("takes");
    expect(reasonLabel("cost_over_limit")).toContain("cost");
    expect(reasonLabel("some_new_code")).toBe("some new code");
  });
  test("age and expiry labels", () => {
    const now = Date.parse("2026-10-16T12:00:00Z");
    expect(ageLabel("2026-10-16T11:59:40Z", now)).toBe("just now");
    expect(ageLabel("2026-10-16T11:30:00Z", now)).toBe("30 min ago");
    expect(ageLabel("2026-10-16T09:00:00Z", now)).toBe("3 h ago");
    expect(ageLabel("2026-10-10T12:00:00Z", now)).toBe("6 d ago");
    expect(expiryLabel("2026-10-17T12:00:00Z", now)).toBe("expires in 24 h");
    expect(expiryLabel("2026-10-16T11:00:00Z", now)).toBe("expired");
    expect(ageLabel("bad", now)).toBe("");
  });
});
