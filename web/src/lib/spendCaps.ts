// The /human/spend caps form: dollar text inputs <-> set_spend_caps body (integer micro-USD only).
import type { components } from "../api/schema";
import { microToInput, parseUSDInput } from "./money";

type CapsView = components["schemas"]["CapsView"];
type SetCaps = components["schemas"]["SetSpendCapsInBody"];
type TokenCapsIn = components["schemas"]["TokenCapsIn"];
type CapToken = components["schemas"]["CapToken"];

/** Server limit: each amount 0..1_000_000_000 micro-USD (C72). */
export const MAX_CAP_MICRO = 1_000_000_000;

export const CAP_FIELDS = [
  { key: "monthly_micro_usd", label: "Monthly cap" },
  { key: "batch_micro_usd", label: "Per-batch cap" },
  { key: "human_daily_micro_usd", label: "Operator daily cap" },
  { key: "agent_daily_micro_usd", label: "Agent daily cap (default)" },
  { key: "auto_approve_micro_usd", label: "Agent auto-approve up to" },
] as const;
export type CapKey = (typeof CAP_FIELDS)[number]["key"];

export interface CapsForm {
  amounts: Record<CapKey, string>;
  autoApproveTakes: string;
}

export function capsToForm(c: CapsView): CapsForm {
  return {
    amounts: {
      monthly_micro_usd: microToInput(c.monthly_micro_usd),
      batch_micro_usd: microToInput(c.batch_micro_usd),
      human_daily_micro_usd: microToInput(c.human_daily_micro_usd),
      agent_daily_micro_usd: microToInput(c.agent_daily_micro_usd),
      auto_approve_micro_usd: microToInput(c.auto_approve_micro_usd),
    },
    autoApproveTakes: String(c.auto_approve_takes),
  };
}

export type CapsResult = { ok: true; body: SetCaps } | { ok: false; error: string };

/** Only changed fields are sent; nothing changed is an error (the server needs at least one field). */
export function formToCaps(f: CapsForm, current: CapsView, tokens: TokenCapsIn[] = []): CapsResult {
  const body: SetCaps = {};
  for (const { key, label } of CAP_FIELDS) {
    const micro = parseUSDInput(f.amounts[key]);
    if (micro === null || micro > MAX_CAP_MICRO) return { ok: false, error: `${label}: enter dollars with at most 6 decimals, up to $${MAX_CAP_MICRO / 1_000_000}.` };
    if (micro !== current[key]) body[key] = micro;
  }
  const t = f.autoApproveTakes.trim();
  if (!/^\d{1,6}$/.test(t)) return { ok: false, error: "Auto-approve takes must be a whole number." };
  if (Number(t) !== current.auto_approve_takes) body.auto_approve_takes = Number(t);
  if (tokens.length) body.tokens = tokens;
  if (Object.keys(body).length === 0) return { ok: false, error: "Nothing changed." };
  return { ok: true, body };
}

export interface TokenLimitText {
  daily: string;
  takes: string;
  autoUSD: string;
}

export function tokenToText(t: CapToken): TokenLimitText {
  return {
    daily: t.daily_budget_micro_usd === null ? "" : microToInput(t.daily_budget_micro_usd),
    takes: t.auto_approve_takes === null ? "" : String(t.auto_approve_takes),
    autoUSD: t.auto_approve_micro_usd === null ? "" : microToInput(t.auto_approve_micro_usd),
  };
}

/** Blank means "use the global default" (null). */
export function textToToken(id: string, t: TokenLimitText): TokenCapsIn | string {
  const amount = (s: string): number | null | undefined => {
    if (s.trim() === "") return null;
    const m = parseUSDInput(s);
    return m === null || m > MAX_CAP_MICRO ? undefined : m;
  };
  const daily = amount(t.daily);
  const auto = amount(t.autoUSD);
  if (daily === undefined || auto === undefined) return "Token limits: enter dollars with at most 6 decimals, or leave blank for the default.";
  const k = t.takes.trim();
  if (k !== "" && !/^\d{1,6}$/.test(k)) return "Token auto-approve takes must be a whole number or blank.";
  return { token_id: id, daily_budget_micro_usd: daily, auto_approve_micro_usd: auto, auto_approve_takes: k === "" ? null : Number(k) };
}
