import { describe, expect, test } from "bun:test";
import { get } from "svelte/store";
import { createSession } from "./session";
import { createApi } from "./client";

function mem() {
  const m = new Map<string, string>();
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
  };
}
const json = () => new Response("{}", { headers: { "content-type": "application/json" } });

function setup(opts: { playing?: boolean; online?: boolean; probe?: () => Response; now?: () => number } = {}) {
  let reloads = 0;
  const probes: string[] = [];
  const session = createSession({
    fetch: async (u) => {
      probes.push(u);
      return (opts.probe ?? json)();
    },
    isPlaying: () => opts.playing ?? false,
    reload: () => void reloads++,
    storage: mem(),
    now: opts.now ?? (() => 1_000_000),
    onLine: () => opts.online ?? true,
    sleep: async () => {},
  });
  return { session, reloads: () => reloads, probes };
}

function client(
  session: ReturnType<typeof createSession>,
  respond: (req: Request, n: number) => Response | Promise<Response>,
) {
  const seen: Request[] = [];
  const api = createApi({
    session,
    baseUrl: "http://localhost",
    fetch: async (req) => {
      seen.push(req);
      return respond(req, seen.length);
    },
  });
  return { api, seen };
}

const typeErr = () => {
  throw new TypeError("Failed to fetch");
};

describe("session loss", () => {
  test("401 is confirmed: paused reloads", async () => {
    const { session, reloads } = setup();
    const { api } = client(session, () => new Response("{}", { status: 401 }));
    await api.GET("/api/ping");
    expect(reloads()).toBe(1);
  });
  test("401 while playing shows the banner and never reloads", async () => {
    const { session, reloads } = setup({ playing: true });
    const { api } = client(session, () => new Response("{}", { status: 401 }));
    await api.GET("/api/ping");
    expect(reloads()).toBe(0);
    expect(get(session.banner)).toBe("expired");
  });
  test("TypeError then 200 does nothing", async () => {
    const { session, reloads, probes } = setup();
    const { api, seen } = client(session, (_r, n) => (n === 1 ? typeErr() : json()));
    const r = await api.GET("/api/ping");
    expect(r.response.status).toBe(200);
    expect(seen).toHaveLength(2);
    expect(reloads()).toBe(0);
    expect(probes).toHaveLength(0);
    expect(get(session.banner)).toBe("none");
  });
  test("TypeError then 401 on retry is confirmed", async () => {
    const { session, reloads } = setup();
    const { api } = client(session, (_r, n) => (n === 1 ? typeErr() : new Response("{}", { status: 401 })));
    await api.GET("/api/ping");
    expect(reloads()).toBe(1);
  });
  test("TypeError twice while offline is transient", async () => {
    const { session, reloads, probes } = setup({ online: false });
    const { api } = client(session, typeErr);
    await expect(api.GET("/api/ping")).rejects.toBeInstanceOf(TypeError);
    expect(reloads()).toBe(0);
    expect(probes).toHaveLength(0);
    expect(get(session.banner)).toBe("offline");
  });
  test("TypeError twice online with an opaqueredirect probe is confirmed", async () => {
    const { session, reloads, probes } = setup({
      probe: () => {
        const r = new Response(null);
        Object.defineProperty(r, "type", { value: "opaqueredirect" });
        return r;
      },
    });
    const { api } = client(session, typeErr);
    await expect(api.GET("/api/ping")).rejects.toBeInstanceOf(TypeError);
    expect(probes).toEqual(["/api/ping"]);
    expect(reloads()).toBe(1);
  });
  test("TypeError twice online with a 200 probe is transient", async () => {
    const { session, reloads } = setup();
    const { api } = client(session, typeErr);
    await expect(api.GET("/api/ping")).rejects.toBeInstanceOf(TypeError);
    expect(reloads()).toBe(0);
    expect(get(session.banner)).toBe("connection");
  });
  test("a probe 401 is confirmed", async () => {
    const { session, reloads } = setup({ probe: () => new Response("{}", { status: 401 }) });
    const { api } = client(session, typeErr);
    await expect(api.GET("/api/ping")).rejects.toBeInstanceOf(TypeError);
    expect(reloads()).toBe(1);
  });
  test("a second confirmed loss within 30 s shows the banner (loop guard)", async () => {
    let t = 1_000_000;
    const { session, reloads } = setup({ now: () => t });
    const { api } = client(session, () => new Response("{}", { status: 401 }));
    await api.GET("/api/ping");
    t += 20_000;
    await api.GET("/api/ping");
    expect(reloads()).toBe(1);
    expect(get(session.banner)).toBe("expired");
  });
  test("three reloads within 10 minutes trip the guard", () => {
    let t = 1_000_000;
    const { session, reloads } = setup({ now: () => t });
    for (let i = 0; i < 4; i++) {
      session.confirmed();
      t += 31_000;
    }
    expect(reloads()).toBe(3);
  });
  test("POST is never retried", async () => {
    const { session } = setup();
    const { api, seen } = client(session, typeErr);
    await expect(api.POST("/api/x" as never, {} as never)).rejects.toBeInstanceOf(TypeError);
    expect(seen).toHaveLength(1);
  });
  test("PUT is retried once with its body intact", async () => {
    const { session } = setup();
    const bodies: string[] = [];
    const { api, seen } = client(session, async (req, n) => {
      bodies.push(await req.clone().text());
      return n === 1 ? typeErr() : json();
    });
    await api.PUT("/api/devices/{device_id}" as never, { params: { path: { device_id: "a" } }, body: { a: 1 } } as never);
    expect(seen).toHaveLength(2);
    expect(bodies[1]).toBe(bodies[0]!);
  });
  test("non-API TypeErrors are not handled", async () => {
    const { session, probes } = setup();
    const { api, seen } = client(session, typeErr);
    await expect(api.GET("/healthz" as never)).rejects.toBeInstanceOf(TypeError);
    expect(seen).toHaveLength(1);
    expect(probes).toHaveLength(0);
  });
});
