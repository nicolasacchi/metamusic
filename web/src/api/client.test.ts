import { describe, expect, test } from "bun:test";
import { get } from "svelte/store";
import { createApi } from "./client";
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
