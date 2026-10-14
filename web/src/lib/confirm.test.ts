import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import { createSession } from "../api/session";
import { confirmButtonLabel, confirmDisabledReasons, createConfirmAction, describeConfirmFailure } from "./confirm";
import type { components } from "../api/schema";

type Estimate = components["schemas"]["Estimate"];
type Handler = (req: Request, n: number) => Response | Promise<Response>;
function rig(handler: Handler) {
  const keys: (string | null)[] = [];
  const paths: string[] = [];
  let n = 0;
  const api = createApi({
    baseUrl: "http://localhost",
    session: createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} }, sleep: async () => {}, onLine: () => true, fetch: async () => new Response("{}") }),
    fetch: async (req) => {
      keys.push(req.headers.get("Idempotency-Key"));
      paths.push(new URL(req.url).pathname);
      return handler(req, n++);
    },
  });
  return { api, keys, paths };
}
const json = (v: unknown, status = 200) =>
  new Response(JSON.stringify(v), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json" } });
const problem = (status: number, code: string, details: Record<string, unknown> = {}) => json({ code, message: code + " message", details }, status);

function est(over: Partial<Estimate> = {}): Estimate {
  return {
    approval_reasons: [],
    backend: "fake",
    budget: {
      batch_cap_micro_usd: 5000000, batch_cap_usd: "5",
      day: { cap_micro_usd: 5000000, cap_usd: "5", left_micro_usd: 5000000, left_usd: "5", used_micro_usd: 0, used_usd: "0" },
      month: { cap_micro_usd: 20000000, cap_usd: "20", left_micro_usd: 20000000, left_usd: "20", used_micro_usd: 0, used_usd: "0" },
      fits: true,
      would_exceed: [],
    },
    confirmation_token: "mmc_tok",
    cost: { attempt_overhead_micro_usd: 0, attempt_overhead_usd: "0", ceiling_display: "$0.70", ceiling_micro_usd: 691731, ceiling_usd: "0.691731", expected_micro_usd: 300000, expected_usd: "0.3", max_micro_usd: 500000, max_usd: "0.5", retry_allowance_micro_usd: 191731, retry_allowance_usd: "0.191731" },
    engine: "yue2",
    estimate_id: "e1",
    eta_s: 100,
    expires_at: "2030-01-01T00:00:00Z",
    items: [],
    price: { gpu_pools: [], observed_at: null, per_hour_micro_usd: 690000, per_hour_usd: "0.69" },
    provider: { armed: true, disabled_reason: null, generation_enabled: true, tripped: false },
    requires_human_approval: false,
    takes: 3,
    warnings: [],
    ...over,
  };
}
const batch = { id: "b1" };

describe("confirm action", () => {
  test("button text is the ceiling rounded up", () => {
    expect(confirmButtonLabel(est())).toBe("Spend up to $0.70");
    const e = est();
    e.cost.ceiling_micro_usd = 324999;
    expect(confirmButtonLabel(e)).toBe("Spend up to $0.33");
    e.cost.ceiling_micro_usd = 5801;
    expect(confirmButtonLabel(e)).toBe("Spend up to $0.0059");
  });

  test("one key per click, reused on retry after a network error", async () => {
    const { api, keys } = rig((_r, n) => {
      if (n === 0) throw new TypeError("network");
      return json(batch, 201);
    });
    const click = createConfirmAction(api, "mmc_tok", "why");
    const first = await click();
    expect(first.kind).toBe("network");
    const second = await click();
    expect(second).toEqual({ kind: "created", batchId: "b1" });
    expect(keys.length).toBe(2);
    expect(keys[0]).toBeTruthy();
    expect(keys[1]).toBe(keys[0]!);
    const other = createConfirmAction(api, "mmc_tok", "why");
    await other();
    expect(keys[2]).not.toBe(keys[0]!);
  });

  test("posts to the human path", async () => {
    const { api, paths } = rig(() => json(batch, 201));
    await createConfirmAction(api, "mmc_tok", "")();
    expect(paths).toEqual(["/human/api/generations"]);
  });

  test("402 renders the cap details", async () => {
    const { api } = rig(() => problem(402, "BUDGET_EXCEEDED", { cap: "monthly", limit_micro_usd: 20000000, limit_usd: "20", used_micro_usd: 19900000, used_usd: "19.9", requested_micro_usd: 691731, requested_usd: "0.691731" }));
    const r = await createConfirmAction(api, "t", "")();
    expect(r.kind).toBe("failed");
    const v = describeConfirmFailure(r as never);
    expect(v.text).toContain("monthly");
    expect(v.text).toContain("$20.00");
    expect(v.text).toContain("$19.90");
    expect(v.text).toContain("$0.70");
    expect(v.reestimate).toBe(false);
  });

  test("410 offers re-estimate", async () => {
    const { api } = rig(() => problem(410, "CONFIRMATION_EXPIRED"));
    const r = await createConfirmAction(api, "t", "")();
    const v = describeConfirmFailure(r as never);
    expect(v.reestimate).toBe(true);
    expect(v.text.toLowerCase()).toContain("re-estimate");
  });

  test("409 with details.batch_id opens that batch", async () => {
    const { api } = rig(() => problem(409, "CONFIRMATION_INVALID", { batch_id: "b9" }));
    const r = await createConfirmAction(api, "t", "")();
    expect(r).toEqual({ kind: "existing", batchId: "b9" });
  });

  test("409 without batch id and 503 are rendered", async () => {
    const a = rig(() => problem(409, "CONFIRMATION_INVALID"));
    const r = await createConfirmAction(a.api, "t", "")();
    expect(r.kind).toBe("failed");
    expect(describeConfirmFailure(r as never).reestimate).toBe(true);
    const b = rig(() => problem(503, "GENERATION_DISABLED", { reason: "backend_none" }));
    const r2 = await createConfirmAction(b.api, "t", "")();
    expect(describeConfirmFailure(r2 as never).text).toContain("GENERATION_DISABLED");
  });
});

describe("confirm disabled reasons", () => {
  test("clean estimate enables confirm", () => {
    expect(confirmDisabledReasons(est())).toEqual([]);
  });
  test("error warnings, disabled/disarmed/tripped, and unfit budget disable it", () => {
    const w = est({ warnings: [{ code: "X", field: "f", message: "bad", severity: "error", take_index: 0 }] });
    expect(confirmDisabledReasons(w).join(" ")).toContain("error");
    expect(confirmDisabledReasons(est({ warnings: [{ code: "X", field: "f", message: "m", severity: "warning", take_index: 0 }] }))).toEqual([]);
    const p = (o: Partial<Estimate["provider"]>) => est({ provider: { armed: true, disabled_reason: null, generation_enabled: true, tripped: false, ...o } });
    expect(confirmDisabledReasons(p({ generation_enabled: false, disabled_reason: "keys_absent" })).join(" ")).toContain("keys_absent");
    expect(confirmDisabledReasons(p({ armed: false })).join(" ")).toContain("disarmed");
    expect(confirmDisabledReasons(p({ tripped: true })).join(" ")).toContain("tripped");
    const e = est();
    e.budget.fits = false;
    e.budget.would_exceed = ["monthly"];
    expect(confirmDisabledReasons(e).join(" ")).toContain("monthly");
  });
});
