import { describe, expect, test } from "bun:test";
import { createApi, RateLimited } from "../api/client";
import { caps, estimate, view } from "./fixtures/t1/transforms";
import {
  EstimateController, RenderController, cancelTransform, createTransform, estimateTransform, getCapabilities, listTransforms, sameLyricsTakes,
  waitForTransform, type EstimateState, type RenderState, type Scheduler, type TRes,
} from "./transformApi";
import type { TransformRequest } from "./transformForm";
import type { TransformEstimate, TransformView } from "./transformView";

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json", ...headers } });
function mockApi(handler: (req: Request) => Response | Promise<Response>) {
  const seen: Request[] = [];
  const api = createApi({
    baseUrl: "http://t.test",
    fetch: async (req) => {
      seen.push(req.clone());
      return handler(req);
    },
  });
  return { api, seen };
}
const body = (kind = "glacier"): TransformRequest => ({ kind, title: null, glacier: { source_id: "t1" } }) as unknown as TransformRequest;

describe("operation wrappers", () => {
  test("capabilities ok", async () => {
    const { api } = mockApi(() => json(200, caps));
    const r = await getCapabilities(api);
    expect(r.ok && r.value.kinds.length).toBe(caps.kinds.length);
  });
  test("501 NOT_IMPLEMENTED is a refusal with the code, never an exception", async () => {
    const { api } = mockApi(() => json(501, { code: "NOT_IMPLEMENTED", message: "not yet", details: { task: "T1-B4" } }));
    const r = await getCapabilities(api);
    expect(r).toMatchObject({ ok: false, status: 501, code: "NOT_IMPLEMENTED", message: "not yet" });
    const e = await estimateTransform(api, body());
    expect(e).toMatchObject({ ok: false, status: 501, code: "NOT_IMPLEMENTED" });
  });
  test("a 501 without a body still reads as NOT_IMPLEMENTED", async () => {
    const { api } = mockApi(() => new Response("", { status: 501 }));
    expect(await getCapabilities(api)).toMatchObject({ ok: false, status: 501, code: "NOT_IMPLEMENTED" });
  });
  test("create sends the idempotency key and the body; the status distinguishes 200 reuse from 202", async () => {
    const { api, seen } = mockApi(() => json(202, view("queued")));
    const r = await createTransform(api, body(), "key-1");
    expect(r).toMatchObject({ ok: true, status: 202 });
    expect(seen[0]!.method).toBe("POST");
    expect(new URL(seen[0]!.url).pathname).toBe("/api/transforms");
    expect(seen[0]!.headers.get("Idempotency-Key")).toBe("key-1");
    expect(await seen[0]!.json()).toEqual(body());
  });
  test("a client-side 429 becomes a RATE_LIMITED refusal with Retry-After", async () => {
    const { api } = mockApi(() => json(429, { code: "TRANSFORM_QUEUE_FULL", message: "full", details: {} }, { "Retry-After": "17" }));
    expect(await createTransform(api, body(), "k")).toMatchObject({ ok: false, status: 429, code: "RATE_LIMITED", retryAfterS: 17 });
    expect(new RateLimited(3).retryAfterS).toBe(3);
  });
  test("network failure is status 0", async () => {
    const api = createApi({ baseUrl: "http://t.test", fetch: () => Promise.reject(new TypeError("down")), session: { ok() {}, confirmed() {}, networkFailure: async () => {}, retryDelay: async () => {} } as never });
    expect(await getCapabilities(api)).toMatchObject({ ok: false, status: 0, code: "NETWORK" });
  });
  test("4xx problem details are kept", async () => {
    const { api } = mockApi(() => json(422, { code: "INVALID_TRANSFORM", message: "bad", details: { field: "factor", rule: "range" } }));
    expect(await estimateTransform(api, body())).toMatchObject({ ok: false, status: 422, code: "INVALID_TRANSFORM", details: { field: "factor" } });
  });
  test("wait, cancel and list use the documented paths and query", async () => {
    const { api, seen } = mockApi((req) => {
      const p = new URL(req.url).pathname;
      if (p.endsWith("/wait")) return json(200, { transform: view("running"), changed: false, timed_out: true });
      if (p.endsWith("/cancel")) return json(200, view("running", { cancel_requested_at: "2026-11-03T10:00:00Z" }));
      return json(200, { items: [view("done")], next_cursor: null });
    });
    await waitForTransform(api, "x1", "queued", 8);
    await cancelTransform(api, "x1");
    await listTransforms(api, { track_id: "t1", limit: 5 });
    const urls = seen.map((r) => new URL(r.url));
    expect(urls[0]!.pathname + urls[0]!.search).toBe("/api/transforms/x1/wait?since_state=queued&timeout_s=8");
    expect(urls[1]!.pathname).toBe("/api/transforms/x1/cancel");
    expect(urls[2]!.search).toBe("?track_id=t1&limit=5");
  });
  test("same-lyrics partners exclude the Take itself and ask lyrics_of", async () => {
    const { api, seen } = mockApi(() => json(200, { items: [{ id: "t1", title: "Self" }, { id: "t2", title: "Other style" }], next_cursor: null }));
    const r = await sameLyricsTakes(api, "t1");
    expect(r).toMatchObject({ ok: true, value: [{ id: "t2", title: "Other style" }] });
    expect(new URL(seen[0]!.url).searchParams.get("lyrics_of")).toBe("t1");
  });
});

class FakeClock implements Scheduler {
  now = 0;
  private timers: { id: number; at: number; fn: () => void }[] = [];
  private n = 0;
  set(fn: () => void, ms: number) {
    const id = ++this.n;
    this.timers.push({ id, at: this.now + ms, fn });
    return id;
  }
  clear(h: unknown) {
    this.timers = this.timers.filter((t) => t.id !== h);
  }
  advance(ms: number) {
    this.now += ms;
    const due = this.timers.filter((t) => t.at <= this.now).sort((a, b) => a.at - b.at);
    this.timers = this.timers.filter((t) => t.at > this.now);
    for (const t of due) t.fn();
  }
  get pending() {
    return this.timers.length;
  }
}
const tick = () => new Promise((r) => setTimeout(r, 0));

describe("debounced estimate", () => {
  function rig(answer: (b: TransformRequest) => Promise<TRes<TransformEstimate>>) {
    const clock = new FakeClock();
    const states: EstimateState[] = [];
    const asked: TransformRequest[] = [];
    const c = new EstimateController({ ask: (b) => (asked.push(b), answer(b)), onState: (s) => states.push(s), scheduler: clock });
    return { c, clock, states, asked };
  }
  const ok = (e = estimate): Promise<TRes<TransformEstimate>> => Promise.resolve({ ok: true, status: 200, value: e });

  test("asks once, 400 ms after the last change", async () => {
    const { c, clock, asked, states } = rig(() => ok());
    c.request(body());
    clock.advance(300);
    c.request({ ...body(), title: "x" });
    clock.advance(399);
    expect(asked.length).toBe(0);
    clock.advance(1);
    await tick();
    expect(asked.length).toBe(1);
    expect(asked[0]!.title).toBe("x");
    expect(states.map((s) => s.kind)).toEqual(["loading", "loading", "ok"]);
  });
  test("the same body is not asked twice; a failed ask is asked again", async () => {
    let n = 0;
    const { c, clock, asked, states } = rig(() => (++n === 1 ? Promise.resolve({ ok: false, status: 422, code: "INVALID_TRANSFORM", message: "bad", details: {} }) : ok()));
    c.request(body());
    clock.advance(400);
    await tick();
    expect(states.at(-1)?.kind).toBe("refused");
    c.request(body());
    clock.advance(400);
    await tick();
    expect(asked.length).toBe(2);
    expect(states.at(-1)?.kind).toBe("ok");
    c.request(body());
    clock.advance(400);
    await tick();
    expect(asked.length).toBe(2);
  });
  test("an invalid form goes idle and cancels the pending ask", async () => {
    const { c, clock, asked, states } = rig(() => ok());
    c.request(body());
    c.request(null);
    clock.advance(1000);
    await tick();
    expect(asked.length).toBe(0);
    expect(states.at(-1)).toEqual({ kind: "idle" });
    expect(clock.pending).toBe(0);
  });
  test("a late answer for an older request is dropped", async () => {
    const slow: { resolve?: (r: TRes<TransformEstimate>) => void } = {};
    let n = 0;
    const { c, clock, states } = rig(() => (++n === 1 ? new Promise((r) => (slow.resolve = r)) : ok({ ...estimate, title: "new" })));
    c.request(body());
    clock.advance(400);
    c.request({ ...body(), title: "second" });
    clock.advance(400);
    await tick();
    slow.resolve?.({ ok: true, status: 200, value: { ...estimate, title: "old" } });
    await tick();
    const last = states.at(-1);
    expect(last).toMatchObject({ kind: "ok", estimate: { title: "new" } });
  });
  test("501 is the unavailable state", async () => {
    const { c, clock, states } = rig(() => Promise.resolve({ ok: false, status: 501, code: "NOT_IMPLEMENTED", message: "", details: {} }));
    c.request(body());
    clock.advance(400);
    await tick();
    expect(states.at(-1)?.kind).toBe("unavailable");
  });
  test("dispose drops an in-flight answer", async () => {
    const { c, clock, states } = rig(() => ok());
    c.request(body());
    clock.advance(400);
    c.dispose();
    await tick();
    expect(states.map((s) => s.kind)).toEqual(["loading"]);
  });
});

describe("render", () => {
  type W = { transform: TransformView; changed: boolean; timed_out: boolean };
  function rig(o: { create: TRes<TransformView>; waits: TRes<W>[]; cancel?: TRes<TransformView> }) {
    const states: RenderState[] = [];
    const sleeps: number[] = [];
    const keys: string[] = [];
    const waited: { since: string; timeout: number }[] = [];
    const waits = [...o.waits];
    let k = 0;
    const c = new RenderController({
      create: async (_b, key) => (keys.push(key), o.create),
      wait: async (_id, since, timeout) => (waited.push({ since, timeout }), waits.shift() ?? { ok: false, status: 0, code: "NETWORK", message: "", details: {} }),
      cancel: async () => o.cancel ?? { ok: true, status: 200, value: view("cancelled") },
      onState: (s) => states.push(s),
      sleep: async (ms) => void sleeps.push(ms),
      newKey: () => `key-${++k}`,
    });
    return { c, states, sleeps, keys, waited };
  }
  const okW = (v: TransformView, changed = true): TRes<W> => ({ ok: true, status: 200, value: { transform: v, changed, timed_out: !changed } });

  test("create, follow through queued -> running -> done, one key per click", async () => {
    const { c, states, keys, waited } = rig({
      create: { ok: true, status: 202, value: view("queued") },
      waits: [okW(view("running")), okW(view("running", { progress_permille: 700 }), false), okW(view("done"))],
    });
    await c.start(body());
    expect(keys).toEqual(["key-1"]);
    expect(states.map((s) => s.kind)).toEqual(["creating", "live", "live", "live", "finished"]);
    expect(waited.map((w) => w.since)).toEqual(["queued", "running", "running"]);
    expect(waited[0]!.timeout).toBe(8);
    expect(states.at(-1)).toMatchObject({ kind: "finished", view: { state: "done" } });
    await c.start(body());
    expect(keys).toEqual(["key-1", "key-2"]);
  });
  test("a reused finished transform ends at once", async () => {
    const { c, states, waited } = rig({ create: { ok: true, status: 200, value: view("done", { reused: true }) }, waits: [] });
    await c.start(body());
    expect(waited.length).toBe(0);
    expect(states.map((s) => s.kind)).toEqual(["creating", "finished"]);
  });
  test("a refused create shows the refusal; 501 is unavailable", async () => {
    const a = rig({ create: { ok: false, status: 429, code: "TRANSFORM_QUOTA_EXCEEDED", message: "", details: { cap_ms: 1 } }, waits: [] });
    await a.c.start(body());
    expect(a.states.at(-1)).toMatchObject({ kind: "refused", refusal: { code: "TRANSFORM_QUOTA_EXCEEDED", details: { cap_ms: 1 } } });
    const b = rig({ create: { ok: false, status: 501, code: "NOT_IMPLEMENTED", message: "", details: {} }, waits: [] });
    await b.c.start(body());
    expect(b.states.at(-1)?.kind).toBe("unavailable");
  });
  test("cancel while running: the follower sees the terminal state, no extra poll after it", async () => {
    let release: (() => void) | undefined;
    const states: RenderState[] = [];
    const c = new RenderController({
      create: async () => ({ ok: true, status: 202, value: view("running") }),
      wait: () => new Promise((r) => (release = () => r({ ok: true, status: 200, value: { transform: view("cancelled"), changed: true, timed_out: false } }))),
      cancel: async () => ({ ok: true, status: 200, value: view("running", { cancel_requested_at: "2026-11-03T10:00:00Z" }) }),
      onState: (s) => states.push(s),
      sleep: async () => {},
    });
    const done = c.start(body());
    await tick();
    await c.cancelNow();
    expect(states.at(-1)).toMatchObject({ kind: "live", view: { cancel_requested_at: "2026-11-03T10:00:00Z" } });
    release?.();
    await done;
    expect(states.at(-1)).toMatchObject({ kind: "finished", view: { state: "cancelled" } });
  });
  test("transient poll failures back off and recover; a run of failures gives up", async () => {
    const fail: TRes<W> = { ok: false, status: 0, code: "NETWORK", message: "", details: {} };
    const r1 = rig({ create: { ok: true, status: 202, value: view("running") }, waits: [fail, fail, okW(view("done"))] });
    await r1.c.start(body());
    expect(r1.sleeps).toEqual([2000, 4000]);
    expect(r1.states.at(-1)?.kind).toBe("finished");
    const r2 = rig({ create: { ok: true, status: 202, value: view("running") }, waits: [fail, fail, fail, fail, fail] });
    await r2.c.start(body());
    expect(r2.states.at(-1)).toMatchObject({ kind: "refused", refusal: { code: "NETWORK" } });
  });
  test("a 404 while following stops at once", async () => {
    const r = rig({ create: { ok: true, status: 202, value: view("running") }, waits: [{ ok: false, status: 404, code: "NOT_FOUND", message: "", details: {} }] });
    await r.c.start(body());
    expect(r.states.at(-1)).toMatchObject({ kind: "refused", refusal: { status: 404 } });
    expect(r.sleeps).toEqual([]);
  });
  test("stop ends following without further states", async () => {
    const r = rig({ create: { ok: true, status: 202, value: view("running") }, waits: [okW(view("running"))] });
    const p = r.c.start(body());
    r.c.stop();
    await p;
    expect(r.states.filter((s) => s.kind === "live").length).toBeLessThanOrEqual(2);
    expect(r.states.some((s) => s.kind === "finished")).toBe(false);
  });
  test("follow resumes a live transform", async () => {
    const r = rig({ create: { ok: false, status: 0, code: "", message: "", details: {} }, waits: [okW(view("done"))] });
    await r.c.follow(view("running"));
    expect(r.states.map((s) => s.kind)).toEqual(["live", "finished"]);
  });
});
