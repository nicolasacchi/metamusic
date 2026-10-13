import { describe, expect, test } from "bun:test";
import { get } from "svelte/store";
import { createApi, RateLimited } from "./client";
import { createSession } from "./session";

function setup(respond: (req: Request) => Response | Promise<Response>, playing = false) {
  const seen: Request[] = [];
  let reloads = 0;
  const session = createSession({
    reload: () => void reloads++,
    isPlaying: () => playing,
    storage: { getItem: () => null, setItem: () => {} },
    sleep: async () => {},
    onLine: () => true,
    fetch: async () => new Response("{}"),
  });
  const api = createApi({
    session,
    baseUrl: "http://localhost",
    fetch: async (req) => {
      seen.push(req);
      return respond(req);
    },
  });
  return { api, seen, session, reloads: () => reloads };
}

const ok = () =>
  new Response(JSON.stringify({ ok: true, version: "t" }), {
    headers: { "content-type": "application/json" },
  });

describe("client middleware", () => {
  test("adds SPA headers", async () => {
    const { api, seen } = setup(ok);
    await api.GET("/api/ping");
    expect(seen).toHaveLength(1);
    expect(seen[0]!.headers.get("X-MM-Client")).toBe("spa");
    expect(seen[0]!.headers.get("X-Requested-With")).toBe("XMLHttpRequest");
    expect(new URL(seen[0]!.url, "http://x").pathname).toBe("/api/ping");
  });

  test("a 401 is a confirmed loss: reload when paused", async () => {
    const { api, reloads } = setup(() => new Response("{}", { status: 401 }));
    await api.GET("/api/ping");
    expect(reloads()).toBe(1);
  });

  test("a 401 while playing shows the banner instead of reloading", async () => {
    const { api, reloads, session } = setup(() => new Response("{}", { status: 401 }), true);
    await api.GET("/api/ping");
    expect(reloads()).toBe(0);
    expect(get(session.banner)).toBe("expired");
  });

  test("does not reload on success or 403", async () => {
    const a = setup(ok);
    await a.api.GET("/api/ping");
    expect(a.reloads()).toBe(0);
    const b = setup(() => new Response("{}", { status: 403 }));
    await b.api.GET("/api/ping");
    expect(b.reloads()).toBe(0);
  });

  test("a single network TypeError is retried once and does not reload", async () => {
    let n = 0;
    const { api, reloads, seen } = setup(() => {
      if (++n === 1) throw new TypeError("Failed to fetch");
      return ok();
    });
    const r = await api.GET("/api/ping");
    expect(r.data?.ok).toBe(true);
    expect(seen).toHaveLength(2);
    expect(reloads()).toBe(0);
  });

  test("repeated TypeErrors with a healthy probe show a connection notice, no reload", async () => {
    const { api, reloads, session } = setup(() => {
      throw new TypeError("Failed to fetch");
    });
    await expect(api.GET("/api/ping")).rejects.toBeInstanceOf(TypeError);
    expect(reloads()).toBe(0);
    expect(get(session.banner)).toBe("connection");
  });
});

describe("429 and step-up handling", () => {
  const problem = (status: number, code: string, headers: Record<string, string> = {}) => () =>
    new Response(JSON.stringify({ code, message: "m", details: {} }), {
      status,
      headers: { "content-type": "application/problem+json", ...headers },
    });

  test("429 RATE_LIMITED throws a typed RateLimited carrying Retry-After and is not session loss", async () => {
    const { api, reloads, session } = setup(problem(429, "RATE_LIMITED", { "Retry-After": "7" }));
    const err = await api.PUT("/api/tracks/{id}/rating", { params: { path: { id: "t" } }, body: { stars: 3 } }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(RateLimited);
    expect((err as RateLimited).retryAfterS).toBe(7);
    expect(reloads()).toBe(0);
    expect(get(session.banner)).toBe("none");
  });

  test("429 without a usable Retry-After falls back to 30 s", async () => {
    const { api } = setup(problem(429, "RATE_LIMITED"));
    const err = await api.GET("/api/ping").catch((e: unknown) => e);
    expect((err as RateLimited).retryAfterS).toBe(30);
  });

  test("403 STEP_UP_REQUIRED navigates to the step-up form", async () => {
    const assigned: string[] = [];
    const seen: string[] = [];
    const api = createApi({
      baseUrl: "http://localhost",
      session: createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} } }),
      fetch: async () => problem(403, "STEP_UP_REQUIRED")(),
      assign: (u) => void assigned.push(u),
      currentPath: () => "/human/tokens?x=1",
    });
    const r = await api.GET("/api/ping");
    seen.push(String(r.response.status));
    expect(assigned).toEqual(["/auth/step-up?next=" + encodeURIComponent("/human/tokens?x=1")]);
  });

  test("403 STEP_UP_REQUIRED on /human/tokens goes to /auth/step-up?next=/human/tokens", async () => {
    const assigned: string[] = [];
    const api = createApi({
      baseUrl: "http://localhost",
      session: createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} } }),
      fetch: async () => problem(403, "STEP_UP_REQUIRED")(),
      assign: (u) => void assigned.push(u),
      currentPath: () => "/human/tokens",
    });
    await api.GET("/human/api/tokens" as never, {} as never).catch(() => {});
    expect(decodeURIComponent(assigned[0] ?? "")).toBe("/auth/step-up?next=/human/tokens");
  });

  test("other 403s do not navigate", async () => {
    const assigned: string[] = [];
    const api = createApi({
      baseUrl: "http://localhost",
      session: createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} } }),
      fetch: async () => problem(403, "FORBIDDEN")(),
      assign: (u) => void assigned.push(u),
    });
    await api.GET("/api/ping");
    expect(assigned).toEqual([]);
  });
});
