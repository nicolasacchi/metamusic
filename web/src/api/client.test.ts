import { describe, expect, test } from "bun:test";
import { get } from "svelte/store";
import { connectionLost, createApi } from "./client";

function memStorage() {
  const m = new Map<string, string>();
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
  };
}

function setup(respond: (req: Request) => Response | Promise<Response>, now = () => 1_000_000) {
  const seen: Request[] = [];
  let reloads = 0;
  const api = createApi({
    storage: memStorage(),
    now,
    baseUrl: "http://localhost",
    fetch: async (req) => {
      seen.push(req);
      return respond(req);
    },
    reload: () => {
      reloads++;
    },
  });
  return { api, seen, reloads: () => reloads };
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

  test("reloads on 401", async () => {
    const { api, reloads } = setup(() => new Response("{}", { status: 401 }));
    await api.GET("/api/ping");
    expect(reloads()).toBe(1);
  });

  test("does not reload on success or 403", async () => {
    const a = setup(ok);
    await a.api.GET("/api/ping");
    expect(a.reloads()).toBe(0);
    const b = setup(() => new Response("{}", { status: 403 }));
    await b.api.GET("/api/ping");
    expect(b.reloads()).toBe(0);
  });

  test("reloads on network TypeError", async () => {
    const { api, reloads } = setup(() => {
      throw new TypeError("Failed to fetch");
    });
    await expect(api.GET("/api/ping")).rejects.toBeInstanceOf(TypeError);
    expect(reloads()).toBe(1);
  });

  test("reloads on TypeError for /human/api but not for non-API URLs", async () => {
    const boom = () => {
      throw new TypeError("Failed to fetch");
    };
    const h = setup(boom);
    await expect(h.api.GET("/human/api/x" as never)).rejects.toBeInstanceOf(TypeError);
    expect(h.reloads()).toBe(1);
    const n = setup(boom);
    await expect(n.api.GET("/healthz" as never)).rejects.toBeInstanceOf(TypeError);
    expect(n.reloads()).toBe(0);
  });

  test("throttles automatic reloads to one per window", async () => {
    let t = 1_000_000;
    const { api, reloads } = setup(() => new Response("{}", { status: 401 }), () => t);
    await api.GET("/api/ping");
    await api.GET("/api/ping");
    expect(reloads()).toBe(1);
    t += 11_000;
    await api.GET("/api/ping");
    expect(reloads()).toBe(2);
  });

  test("flags connection lost when a reload is throttled", async () => {
    connectionLost.set(false);
    const { api } = setup(() => new Response("{}", { status: 401 }));
    await api.GET("/api/ping");
    expect(get(connectionLost)).toBe(false);
    await api.GET("/api/ping");
    expect(get(connectionLost)).toBe(true);
  });
});
