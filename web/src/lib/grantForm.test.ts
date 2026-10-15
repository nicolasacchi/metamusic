import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import { buildGrantBody, formToScopes, grantToForm, saveGrant, toggleScope } from "./grantForm";

type Grant = components["schemas"]["ConnectorGrant"];

function grant(over: Partial<Grant> = {}): Grant {
  return {
    client_id: "c1",
    created_at: "2026-01-01T00:00:00Z",
    effective: { auto_approve_micro_usd: 0, auto_approve_takes: 0, auto_approve_usd: "$0.00", daily_budget_micro_usd: 1, daily_budget_usd: "$0.00" },
    generate_enabled: false,
    id: "g1",
    label: "Test client",
    last_used_at: null,
    limits: { auto_approve_micro_usd: null, auto_approve_takes: null, auto_approve_usd: null, daily_budget_micro_usd: null, daily_budget_usd: null },
    revoked_at: null,
    revoked_by: null,
    scopes: ["read"],
    subject_hint: null,
    updated_at: "2026-01-01T00:00:00Z",
    updated_by: { id: "human", kind: "human", name: "human" } as Grant["updated_by"],
    username: null,
    ...over,
  };
}

describe("grant form", () => {
  test("scopes always include read, even when unchecked", () => {
    const f = grantToForm(grant({ scopes: ["read", "write"] }));
    expect(formToScopes(f)).toEqual(["read", "write"]);
    expect(formToScopes(toggleScope(f, "read"))).toEqual(["read", "write"]);
    expect(formToScopes({ ...f, read: false, write: false, generate: true })).toEqual(["read", "generate"]);
  });
  test("toggleScope flips write and generate", () => {
    const f = grantToForm(grant());
    const g = toggleScope(toggleScope(f, "write"), "generate");
    expect(formToScopes(g)).toEqual(["read", "write", "generate"]);
    expect(formToScopes(toggleScope(g, "write"))).toEqual(["read", "generate"]);
  });
  test("empty limits mean null (the global default)", () => {
    const r = buildGrantBody(grantToForm(grant()));
    expect(r).toEqual({ ok: true, body: { scopes: ["read"], limits: { daily_budget_micro_usd: null, auto_approve_micro_usd: null, auto_approve_takes: null } } });
  });
  test("dollar inputs become integer micro-USD via money.ts", () => {
    const f = { ...grantToForm(grant()), dailyUsd: "1.25", autoApproveUsd: "0.000001", autoApproveTakes: "3" };
    const r = buildGrantBody(f);
    expect(r).toEqual({ ok: true, body: { scopes: ["read"], limits: { daily_budget_micro_usd: 1_250_000, auto_approve_micro_usd: 1, auto_approve_takes: 3 } } });
  });
  test("existing limits populate the inputs exactly", () => {
    const f = grantToForm(grant({ limits: { auto_approve_micro_usd: 500_000, auto_approve_takes: 2, auto_approve_usd: "$0.50", daily_budget_micro_usd: 2_000_000, daily_budget_usd: "$2.00" } }));
    expect([f.dailyUsd, f.autoApproveUsd, f.autoApproveTakes]).toEqual(["2", "0.5", "2"]);
  });
  test("invalid amounts are rejected", () => {
    for (const bad of ["-1", "1e3", "abc", "1.0000001"]) {
      expect(buildGrantBody({ ...grantToForm(grant()), dailyUsd: bad }).ok).toBe(false);
    }
    expect(buildGrantBody({ ...grantToForm(grant()), autoApproveTakes: "1.5" }).ok).toBe(false);
    expect(buildGrantBody({ ...grantToForm(grant()), autoApproveTakes: "-2" }).ok).toBe(false);
  });
  test("one set_connector_grant per Save", async () => {
    const calls: Array<{ id: string; body: unknown }> = [];
    const put = async (id: string, body: unknown) => {
      calls.push({ id, body });
      return { data: grant() };
    };
    const f = { ...grantToForm(grant()), dailyUsd: "1" };
    const r = await saveGrant(put as never, "g1", f);
    expect(r.ok).toBe(true);
    expect(calls).toHaveLength(1);
    expect(calls[0]!.id).toBe("g1");
  });
  test("an invalid form makes no call", async () => {
    let n = 0;
    const put = async () => {
      n++;
      return {};
    };
    const r = await saveGrant(put as never, "g1", { ...grantToForm(grant()), dailyUsd: "x" });
    expect(r.ok).toBe(false);
    expect(n).toBe(0);
  });
});
