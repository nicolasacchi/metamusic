import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import type { createSession } from "../api/session";
import { checkAbc, editPlan, failure, getPlan, listPlans, openTakeInDesk, setVerdict } from "./deskApi";

function fake(handler: (req: Request) => Response) {
  const seen: Request[] = [];
  const api = createApi({
    baseUrl: "http://x.test",
    fetch: (req) => {
      seen.push(req.clone());
      return Promise.resolve(handler(req));
    },
    session: { ok() {}, confirmed() {}, networkFailure: () => Promise.resolve(), retryDelay: () => Promise.resolve() } as unknown as ReturnType<typeof createSession>,
  });
  return { api, seen };
}
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
const problem = (status: number, code: string, details: unknown = {}) => json(status, { code, message: `${code} text`, details });

describe("deskApi", () => {
  test("listPlans sends the filters and returns the page", async () => {
    const { api, seen } = fake(() => json(200, { items: [{ id: "p1" }], next_cursor: "n" }));
    const r = await listPlans(api, { source: "job", job_id: "j1", rendered: false, limit: 100 });
    const u = new URL(seen[0]!.url);
    expect(u.pathname).toBe("/api/plans");
    expect([u.searchParams.get("source"), u.searchParams.get("job_id"), u.searchParams.get("rendered")]).toEqual(["job", "j1", "false"]);
    expect(r.ok && r.value.items.map((p) => p.id)).toEqual(["p1"]);
    expect(r.ok && r.value.next).toBe("n");
  });
  test("listPlans: a null items list is an empty one", async () => {
    const { api } = fake(() => json(200, { items: null, next_cursor: null }));
    expect(await listPlans(api)).toEqual({ ok: true, value: { items: [], next: null } });
  });
  test("a Listener's 403 comes back as a failure, not a throw", async () => {
    const { api } = fake(() => problem(403, "FORBIDDEN"));
    const r = await listPlans(api);
    expect(r.ok).toBe(false);
    if (!r.ok) expect([r.status, r.code]).toEqual([403, "FORBIDDEN"]);
  });
  test("getPlan asks for the score, the bars and the display rewrite; 404 is NOT_FOUND", async () => {
    const { api, seen } = fake((req) => (req.url.includes("/missing") ? problem(404, "NOT_FOUND") : json(200, { id: "p1" })));
    expect((await getPlan(api, "p1")).ok).toBe(true);
    expect(new URL(seen[0]!.url).searchParams.get("include")).toBe("abc,bars,display");
    const r = await getPlan(api, "missing");
    expect(r.ok === false && r.code).toBe("NOT_FOUND");
  });
  test("checkAbc posts the score (and the base) as yue2", async () => {
    const { api, seen } = fake(() => json(200, { ok: true }));
    await checkAbc(api, "X:1", "X:0");
    expect(await seen[0]!.json()).toEqual({ abc: "X:1", base_abc: "X:0", dialect: "yue2" });
  });
  test("openTakeInDesk: one key per action, 201 is created, 200 is the existing plan", async () => {
    let n = 0;
    const { api, seen } = fake(() => json(n++ === 0 ? 201 : 200, { id: "p1" }));
    const act = openTakeInDesk(api, "t1");
    const a = await act();
    const b = await act();
    expect(seen[0]!.headers.get("Idempotency-Key")).toBe(seen[1]!.headers.get("Idempotency-Key"));
    expect(new URL(seen[0]!.url).pathname).toBe("/api/tracks/t1/plan");
    expect([a.ok && a.value.created, b.ok && b.value.created]).toEqual([true, false]);
  });
  test("openTakeInDesk keeps the reason of PLAN_SOURCE_UNSUPPORTED", async () => {
    const { api } = fake(() => problem(422, "PLAN_SOURCE_UNSUPPORTED", { reason: "no_score" }));
    const r = await openTakeInDesk(api, "t1")();
    expect(r.ok === false && r.details).toEqual({ reason: "no_score" });
  });
  test("editPlan posts the ops with a rationale only when there is one", async () => {
    const { api, seen } = fake(() => json(201, { id: "p2" }));
    await editPlan(api, "p1", [{ op: "set_tempo", bpm: 90 }])();
    await editPlan(api, "p1", [{ op: "set_tempo", bpm: 90 }], "slower")();
    expect(new URL(seen[0]!.url).pathname).toBe("/api/plans/p1/edits");
    expect(await seen[0]!.json()).toEqual({ ops: [{ op: "set_tempo", bpm: 90 }] });
    expect(await seen[1]!.json()).toEqual({ ops: [{ op: "set_tempo", bpm: 90 }], rationale: "slower" });
  });
  test("editPlan: a refusal keeps its details for the editor", async () => {
    const { api } = fake(() => problem(422, "PLAN_EDIT_INVALID", { rule: "invariants", violations: [{ bar: 0 }] }));
    const r = await editPlan(api, "p1", [])();
    expect(r.ok === false && r.code).toBe("PLAN_EDIT_INVALID");
    expect(r.ok === false && Array.isArray(r.details.violations)).toBe(true);
  });
  test("setVerdict sends null to clear", async () => {
    const { api, seen } = fake(() => json(200, { id: "p1" }));
    await setVerdict(api, "p1", null);
    await setVerdict(api, "p1", "liked", "warm");
    expect(await seen[0]!.json()).toEqual({ verdict: null });
    expect(await seen[1]!.json()).toEqual({ verdict: "liked", note: "warm" });
    expect(seen[0]!.method).toBe("PUT");
  });
  test("a network error is a NETWORK failure", async () => {
    const api = createApi({ baseUrl: "http://x.test", fetch: () => Promise.reject(new TypeError("down")), session: { ok() {}, confirmed() {}, networkFailure: () => Promise.resolve(), retryDelay: () => Promise.resolve() } as unknown as ReturnType<typeof createSession> });
    const r = await getPlan(api, "p1");
    expect(r.ok === false && r.code).toBe("NETWORK");
  });
  test("failure tolerates a body that is not a problem", () => {
    expect(failure(502, "bad gateway", "fallback")).toEqual({ ok: false, status: 502, code: "", message: "fallback", details: {} });
    expect(failure(500, { code: "X", details: [1] }, "f").details).toEqual({});
  });
});
