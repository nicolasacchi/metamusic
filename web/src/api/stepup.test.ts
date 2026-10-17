import { describe, expect, test } from "bun:test";
import { get } from "svelte/store";
import { createApi } from "./client";
import { createSession } from "./session";
import { createStepUp, EDGE_URL, isDoneMessage, isStepUpTab, LOCAL_URL, type StepUpDeps } from "./stepup";

interface Rig {
  deps: StepUpDeps;
  log: string[];
  opened: string[];
  assigned: string[];
  signal: (verify: boolean) => void;
  gate: { open: boolean };
}

function rig(over: Partial<StepUpDeps> = {}): Rig {
  const log: string[] = [];
  const opened: string[] = [];
  const assigned: string[] = [];
  const gate = { open: false };
  let sig: (verify: boolean) => void = () => {};
  const deps: StepUpDeps = {
    open: (u) => (opened.push(u), true),
    assign: (u) => void assigned.push(u),
    here: () => "/human/spend",
    snapshot: () => void log.push("snapshot"),
    standalone: () => false,
    listen: (s) => ((sig = s), () => void log.push("unlisten")),
    gateOpen: async () => gate.open,
    completed: () => void log.push("completed"),
    ...over,
  };
  return { deps, log, opened, assigned, signal: (v) => sig(v), gate };
}

describe("step-up flow", () => {
  test("snapshots, opens the second tab, waits, and resolves true on the done message", async () => {
    const r = rig();
    const s = createStepUp(r.deps);
    const p = s.require("local");
    expect(r.log).toEqual(["snapshot"]);
    expect(r.opened).toEqual([LOCAL_URL]);
    expect(r.assigned).toEqual([]);
    expect(get(s).waiting).toBe("local");
    r.signal(false);
    expect(await p).toBe(true);
    expect(get(s).waiting).toBeNull();
    expect(r.log).toEqual(["snapshot", "unlisten", "completed"]);
    expect(r.assigned).toEqual([]);
  });

  test("the edge trigger opens /human/?mm_stepup=1", async () => {
    const r = rig();
    const s = createStepUp(r.deps);
    void s.require("edge");
    expect(r.opened).toEqual([EDGE_URL]);
  });

  test("concurrent callers share one tab and one wait", async () => {
    const r = rig();
    const s = createStepUp(r.deps);
    const a = s.require("local");
    const b = s.require("local");
    expect(a).toBe(b);
    expect(r.opened).toHaveLength(1);
    r.signal(false);
    expect(await Promise.all([a, b])).toEqual([true, true]);
  });

  test("a visibility signal retries only when the gate really is open", async () => {
    const r = rig();
    const s = createStepUp(r.deps);
    let settled: boolean | null = null;
    void s.require("local").then((v) => (settled = v));
    r.signal(true);
    await new Promise((x) => setTimeout(x, 5));
    expect(settled as boolean | null).toBeNull();
    expect(get(s).waiting).toBe("local");
    r.gate.open = true;
    r.signal(true);
    await new Promise((x) => setTimeout(x, 5));
    expect(settled as boolean | null).toBe(true);
  });

  test("popup blocked: snapshot, then the full-page hop to the step-up form with the current page as next", async () => {
    const r = rig({ open: () => false });
    const s = createStepUp(r.deps);
    expect(await s.require("local")).toBe(false);
    expect(r.log).toEqual(["snapshot"]);
    expect(r.assigned).toEqual(["/auth/step-up?next=" + encodeURIComponent("/human/spend")]);
    expect(get(s).waiting).toBeNull();
  });

  test("popup blocked at the edge: the hop is a document load of the page itself (the edge asks there)", async () => {
    const r = rig({ open: () => false });
    expect(await createStepUp(r.deps).require("edge")).toBe(false);
    expect(r.assigned).toEqual(["/human/spend"]);
  });

  test("installed iOS app: never opens a second tab", async () => {
    const r = rig({ standalone: () => true });
    expect(await createStepUp(r.deps).require("local")).toBe(false);
    expect(r.opened).toEqual([]);
    expect(r.assigned).toHaveLength(1);
  });

  test("cancel resolves false and stops listening; reopen after a closed tab opens again from the click", async () => {
    const r = rig();
    const s = createStepUp(r.deps);
    const p = s.require("local");
    s.reopen();
    expect(r.opened).toEqual([LOCAL_URL, LOCAL_URL]);
    s.cancel();
    expect(await p).toBe(false);
    expect(r.log).toContain("unlisten");
    expect(get(s).waiting).toBeNull();
  });

  test("done message shape and the proxy second-tab URL are strict", () => {
    expect(isDoneMessage({ type: "done" })).toBe(true);
    for (const m of [null, "done", { type: "nope" }, {}, 1]) expect(isDoneMessage(m)).toBe(false);
    expect(isStepUpTab({ pathname: "/human/", search: "?mm_stepup=1" })).toBe(true);
    expect(isStepUpTab({ pathname: "/human", search: "?mm_stepup=1" })).toBe(true);
    expect(isStepUpTab({ pathname: "/human/", search: "" })).toBe(false);
    expect(isStepUpTab({ pathname: "/tracks/x", search: "?mm_stepup=1" })).toBe(false);
  });
});

describe("client retries the gated request after the second tab finishes", () => {
  const problem = () =>
    new Response(JSON.stringify({ code: "STEP_UP_REQUIRED", message: "m", details: {} }), { status: 403, headers: { "content-type": "application/problem+json" } });
  const okJson = () => new Response(JSON.stringify({ id: "b1" }), { status: 200, headers: { "content-type": "application/json" } });
  const mkSession = () => createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} }, sleep: async () => {} });

  test("local: 403 STEP_UP_REQUIRED, confirmation elsewhere, the same request (body and idempotency key) is replayed", async () => {
    const r = rig();
    const seen: { body: string; key: string | null }[] = [];
    let calls = 0;
    const api = createApi({
      baseUrl: "http://localhost",
      session: mkSession(),
      stepUp: createStepUp(r.deps),
      fetch: async (req) => {
        calls++;
        seen.push({ body: await req.clone().text(), key: req.headers.get("Idempotency-Key") });
        return calls === 1 ? problem() : okJson();
      },
    });
    const pending = api.POST("/human/api/tokens", { params: { header: { "Idempotency-Key": "k1" } }, body: { name: "n", scopes: ["read"] } });
    await new Promise((x) => setTimeout(x, 5));
    expect(r.opened).toEqual([LOCAL_URL]);
    r.signal(false);
    const res = await pending;
    expect(res.response.status).toBe(200);
    expect(seen).toHaveLength(2);
    expect(seen[1]).toEqual(seen[0]!);
    expect(seen[1]!.body).toContain('"name":"n"');
    expect(seen[1]!.key).toBe("k1");
  });

  test("edge: a failed fetch on /human/api while the app answers /api/ping is a step-up, not an outage", async () => {
    const r = rig();
    let human = 0;
    const session = mkSession();
    const api = createApi({
      baseUrl: "http://localhost",
      session,
      stepUp: createStepUp(r.deps),
      fetch: async (req) => {
        const p = new URL(req.url).pathname;
        if (p === "/api/ping") return new Response("{}", { status: 200 });
        human++;
        if (human === 1) throw new TypeError("Failed to fetch");
        return okJson();
      },
    });
    const pending = api.POST("/human/api/batches/{id}/approve", { params: { path: { id: "b1" } }, body: {} });
    await new Promise((x) => setTimeout(x, 5));
    expect(r.opened).toEqual([EDGE_URL]);
    expect(get(session.banner)).toBe("none");
    r.signal(false);
    expect((await pending).response.status).toBe(200);
    expect(human).toBe(2);
  });

  test("edge: a 401 on /human/api with a working /api/ping is a step-up; with a dead session it is session loss", async () => {
    const r = rig();
    let n = 0;
    const api = createApi({
      baseUrl: "http://localhost",
      session: mkSession(),
      stepUp: createStepUp(r.deps),
      fetch: async (req) => {
        if (new URL(req.url).pathname === "/api/ping") return new Response("{}", { status: 200 });
        return ++n === 1 ? new Response("denied", { status: 401 }) : okJson();
      },
    });
    const pending = api.GET("/human/api/devices");
    await new Promise((x) => setTimeout(x, 5));
    expect(r.opened).toEqual([EDGE_URL]);
    r.signal(false);
    expect((await pending).response.status).toBe(200);

    const lost = rig();
    let reloaded = 0;
    const api2 = createApi({
      baseUrl: "http://localhost",
      session: createSession({ reload: () => void reloaded++, storage: { getItem: () => null, setItem: () => {} } }),
      stepUp: createStepUp(lost.deps),
      fetch: async () => new Response("denied", { status: 401 }),
    });
    expect((await api2.GET("/human/api/devices")).response.status).toBe(401);
    expect(lost.opened).toEqual([]);
    expect(reloaded).toBe(1);
  });

  test("a genuine outage (the app does not answer either) never opens a confirmation", async () => {
    const r = rig();
    const api = createApi({
      baseUrl: "http://localhost",
      session: mkSession(),
      stepUp: createStepUp(r.deps),
      fetch: async () => {
        throw new TypeError("Failed to fetch");
      },
    });
    await api.GET("/human/api/devices").catch(() => {});
    expect(r.opened).toEqual([]);
    expect(r.assigned).toEqual([]);
  });

  test("a gate that stays closed after the user gives up leaves the original 403 for the page to show", async () => {
    const r = rig();
    const s = createStepUp(r.deps);
    const api = createApi({ baseUrl: "http://localhost", session: mkSession(), stepUp: s, fetch: async () => problem() });
    const pending = api.GET("/human/api/devices");
    await new Promise((x) => setTimeout(x, 5));
    s.cancel();
    expect((await pending).response.status).toBe(403);
  });
});
