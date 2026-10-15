// The /human/connectors grant row: form state <-> set_connector_grant body (integer micro-USD only).
import type { components } from "../api/schema";
import { microToInput, parseUSDInput } from "./money";

type Grant = components["schemas"]["ConnectorGrant"];
type SetBody = components["schemas"]["SetConnectorGrantInBody"];
type Scope = "read" | "write" | "generate";

export interface GrantForm {
  read: boolean;
  write: boolean;
  generate: boolean;
  dailyUsd: string;
  autoApproveUsd: string;
  autoApproveTakes: string;
}

export function grantToForm(g: Grant): GrantForm {
  return {
    read: true,
    write: g.scopes.includes("write"),
    generate: g.scopes.includes("generate"),
    dailyUsd: g.limits.daily_budget_micro_usd === null ? "" : microToInput(g.limits.daily_budget_micro_usd),
    autoApproveUsd: g.limits.auto_approve_micro_usd === null ? "" : microToInput(g.limits.auto_approve_micro_usd),
    autoApproveTakes: g.limits.auto_approve_takes === null ? "" : String(g.limits.auto_approve_takes),
  };
}

/** read is always included (the server requires it). */
export function formToScopes(f: GrantForm): Scope[] {
  const s: Scope[] = ["read"];
  if (f.write) s.push("write");
  if (f.generate) s.push("generate");
  return s;
}

export function toggleScope(f: GrantForm, s: Scope): GrantForm {
  if (s === "read") return f;
  return { ...f, [s]: !f[s] };
}

export type GrantResult = { ok: true; body: SetBody } | { ok: false; error: string };

function usd(text: string, label: string): { v: number | null; err?: string } {
  if (text.trim() === "") return { v: null };
  const m = parseUSDInput(text);
  return m === null ? { v: null, err: `${label}: enter dollars like 0.50` } : { v: m };
}

export function buildGrantBody(f: GrantForm): GrantResult {
  const d = usd(f.dailyUsd, "Daily budget");
  if (d.err) return { ok: false, error: d.err };
  const a = usd(f.autoApproveUsd, "Auto-approve");
  if (a.err) return { ok: false, error: a.err };
  let takes: number | null = null;
  if (f.autoApproveTakes.trim() !== "") {
    if (!/^\d{1,9}$/.test(f.autoApproveTakes.trim())) return { ok: false, error: "Auto-approve takes: a whole number" };
    takes = Number(f.autoApproveTakes.trim());
  }
  return {
    ok: true,
    body: { scopes: formToScopes(f), limits: { daily_budget_micro_usd: d.v, auto_approve_micro_usd: a.v, auto_approve_takes: takes } },
  };
}

type Put = (id: string, body: SetBody) => Promise<{ data?: Grant; error?: { message?: string } }>;

/** One Save = exactly one set_connector_grant call; an invalid form makes none. */
export async function saveGrant(put: Put, id: string, f: GrantForm): Promise<{ ok: true; grant: Grant } | { ok: false; error: string }> {
  const b = buildGrantBody(f);
  if (!b.ok) return b;
  try {
    const r = await put(id, b.body);
    return r.data ? { ok: true, grant: r.data } : { ok: false, error: r.error?.message ?? "Could not save the grant" };
  } catch {
    return { ok: false, error: "Could not save the grant" };
  }
}
