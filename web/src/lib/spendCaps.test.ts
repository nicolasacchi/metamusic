import { describe, expect, test } from "bun:test";
import { capsToForm, formToCaps, textToToken, tokenToText } from "./spendCaps";
import type { components } from "../api/schema";

const caps = {
  monthly_micro_usd: 20_000_000, monthly_usd: "20",
  batch_micro_usd: 5_000_000, batch_usd: "5",
  human_daily_micro_usd: 5_000_000, human_daily_usd: "5",
  agent_daily_micro_usd: 2_000_000, agent_daily_usd: "2",
  auto_approve_micro_usd: 500_000, auto_approve_usd: "0.5",
  auto_approve_takes: 3,
  tokens: [], updated_at: "x", updated_by: { id: "h", kind: "human", name: "h" },
} as components["schemas"]["CapsView"];

describe("spend caps form", () => {
  test("unchanged form is refused, changed fields only are sent", () => {
    const f = capsToForm(caps);
    expect(f.amounts.auto_approve_micro_usd).toBe("0.5");
    expect(formToCaps(f, caps)).toEqual({ ok: false, error: "Nothing changed." });
    f.amounts.monthly_micro_usd = "25.50";
    f.autoApproveTakes = "4";
    expect(formToCaps(f, caps)).toEqual({ ok: true, body: { monthly_micro_usd: 25_500_000, auto_approve_takes: 4 } });
  });
  test("invalid amounts and ceilings are refused", () => {
    const f = capsToForm(caps);
    f.amounts.batch_micro_usd = "0.1234567";
    expect(formToCaps(f, caps).ok).toBe(false);
    f.amounts.batch_micro_usd = "1001";
    expect(formToCaps(f, caps).ok).toBe(false);
    f.amounts.batch_micro_usd = "1000";
    expect(formToCaps(f, caps).ok).toBe(true);
  });
  test("token limits: blank is null", () => {
    expect(textToToken("t1", { daily: "", takes: "", autoUSD: "" })).toEqual({ token_id: "t1", daily_budget_micro_usd: null, auto_approve_micro_usd: null, auto_approve_takes: null });
    expect(textToToken("t1", { daily: "1.25", takes: "2", autoUSD: "0.1" })).toEqual({ token_id: "t1", daily_budget_micro_usd: 1_250_000, auto_approve_micro_usd: 100_000, auto_approve_takes: 2 });
    expect(typeof textToToken("t1", { daily: "x", takes: "", autoUSD: "" })).toBe("string");
    expect(tokenToText({ name: "n", token_id: "t", daily_budget_micro_usd: 1_250_000, daily_budget_usd: "1.25", auto_approve_micro_usd: null, auto_approve_takes: 2, auto_approve_usd: null })).toEqual({ daily: "1.25", takes: "2", autoUSD: "" });
  });
});
