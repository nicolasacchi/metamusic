import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import { createSession } from "../api/session";
import { createJudge, createTagSuggestions, starsFromKey, type TrackJudgment } from "./judge.svelte";

type Handler = (req: Request, body: unknown) => Response | Promise<Response>;

function rig(handler: Handler) {
  const reqs: { method: string; path: string; body: unknown; headers: Headers }[] = [];
  const api = createApi({
    baseUrl: "http://localhost",
    session: createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} }, sleep: async () => {}, onLine: () => true, fetch: async () => new Response("{}") }),
    fetch: async (req) => {
      const text = req.method === "GET" ? "" : await req.clone().text();
      const body = text ? JSON.parse(text) : undefined;
      reqs.push({ method: req.method, path: new URL(req.url).pathname, body, headers: req.headers });
      return handler(req, body);
    },
  });
  const toasts: string[] = [];
  const judge = createJudge({ api, notify: (m) => void toasts.push(m) });
  return { api, judge, reqs, toasts };
}

const json = (v: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(v), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json", ...headers } });
const problem = (status: number, code: string, headers: Record<string, string> = {}) => json({ code, message: "boom", details: {} }, status, headers);

const base: TrackJudgment = {
  stars: 2,
  verdict: null,
  critic: { stars: 3, verdict: "solid" },
  status: "draft",
  tags: [{ name: "a", sources: ["human"] }],
};
const who = { id: "u", kind: "human", name: "op" };

describe("judge store", () => {
  test("rate is optimistic and then confirmed by the server", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const { judge, reqs } = rig(async () => {
      await gate;
      return json({ track_id: "t", human: { by: who, stars: 4, updated_at: "x", verdict: null }, critic: { by: who, stars: 3, updated_at: "x", verdict: "solid" } });
    });
    judge.seed("t", base);
    const p = judge.rate("t", 4);
    expect(judge.get("t")!.stars).toBe(4);
    release();
    expect(await p).toBe(true);
    expect(judge.get("t")!.stars).toBe(4);
    expect(reqs[0]!.method).toBe("PUT");
    expect(reqs[0]!.path).toBe("/api/tracks/t/rating");
    expect(reqs[0]!.body).toEqual({ stars: 4 });
  });

  test("clearing sends stars null", async () => {
    const { judge, reqs } = rig(() => json({ track_id: "t", human: null, critic: null }));
    judge.seed("t", base);
    await judge.rate("t", null);
    expect(reqs[0]!.body).toEqual({ stars: null });
    expect(judge.get("t")!.stars).toBeNull();
    expect(judge.get("t")!.critic).toBeNull();
  });

  test("500 rolls back and toasts", async () => {
    const { judge, toasts } = rig(() => problem(500, "INTERNAL"));
    judge.seed("t", base);
    expect(await judge.rate("t", 5)).toBe(false);
    expect(judge.get("t")!.stars).toBe(2);
    expect(toasts).toHaveLength(1);
    expect(toasts[0]).toContain("boom");
  });

  test("429 rolls back, names Retry-After seconds and never retries", async () => {
    const { judge, toasts, reqs } = rig(() => problem(429, "RATE_LIMITED", { "Retry-After": "12" }));
    judge.seed("t", base);
    expect(await judge.rate("t", 5)).toBe(false);
    expect(judge.get("t")!.stars).toBe(2);
    expect(toasts).toEqual(["Too many changes — try again in 12 s"]);
    expect(reqs).toHaveLength(1);
  });

  test("status update rolls back on 429", async () => {
    const { judge, toasts } = rig(() => problem(429, "RATE_LIMITED", { "Retry-After": "3" }));
    judge.seed("t", base);
    await judge.setStatus("t", "final");
    expect(judge.get("t")!.status).toBe("draft");
    expect(toasts[0]).toContain("3 s");
  });

  test("status update sends PATCH and keeps the new status", async () => {
    const { judge, reqs } = rig(() => json({ id: "t", status: "final" }));
    judge.seed("t", base);
    expect(await judge.setStatus("t", "final")).toBe(true);
    expect(reqs[0]!.method).toBe("PATCH");
    expect(reqs[0]!.body).toEqual({ status: "final" });
    expect(judge.get("t")!.status).toBe("final");
  });

  test("tags: add shows at once, server list wins; remove rolls back on failure", async () => {
    let n = 0;
    const { judge, reqs, toasts } = rig(() => {
      n++;
      return n === 1
        ? json({ track_id: "t", tags: [{ name: "a", sources: ["human"] }, { name: "b", sources: ["human"] }] })
        : problem(500, "INTERNAL");
    });
    judge.seed("t", base);
    await judge.addTag("t", " B ");
    expect(reqs[0]!.body).toEqual({ add: ["b"] });
    expect(judge.get("t")!.tags.map((x) => x.name)).toEqual(["a", "b"]);
    await judge.removeTag("t", "a");
    expect(reqs[1]!.body).toEqual({ remove: ["a"] });
    expect(judge.get("t")!.tags.map((x) => x.name)).toEqual(["a", "b"]);
    expect(toasts).toHaveLength(1);
  });

  test("adding an existing or empty tag sends nothing", async () => {
    const { judge, reqs } = rig(() => json({}));
    judge.seed("t", base);
    await judge.addTag("t", "a");
    await judge.addTag("t", "  ");
    expect(reqs).toHaveLength(0);
  });

  test("the critic is read-only: ratings never send it and a human rating leaves it intact", async () => {
    const { judge, reqs } = rig(() => json({ track_id: "t", human: { by: who, stars: 1, updated_at: "x", verdict: null }, critic: { by: who, stars: 3, updated_at: "x", verdict: "solid" } }));
    judge.seed("t", base);
    await judge.rate("t", 1);
    expect(JSON.stringify(reqs[0]!.body)).not.toContain("critic");
    expect(judge.get("t")!.critic).toEqual({ stars: 3, verdict: "solid" });
  });
});

describe("tag suggestions", () => {
  test("one list_tags call serves every consumer", async () => {
    const { api, reqs } = rig(() => json({ items: [{ name: "x", tracks: 2, by_source: {} }, { name: "y", tracks: 1, by_source: {} }] }));
    const s = createTagSuggestions(api);
    expect(await s.names()).toEqual(["x", "y"]);
    expect(await s.names()).toEqual(["x", "y"]);
    expect(reqs.filter((r) => r.path === "/api/tags")).toHaveLength(1);
  });
  test("a failed fetch can be retried", async () => {
    let n = 0;
    const { api } = rig(() => (++n === 1 ? problem(500, "X") : json({ items: [{ name: "x", tracks: 1, by_source: {} }] })));
    const s = createTagSuggestions(api);
    expect(await s.names()).toEqual([]);
    expect(await s.names()).toEqual(["x"]);
  });
  test("filter suggests unused names by prefix first", () => {
    const s = createTagSuggestions(rig(() => json({})).api);
    expect(s.filter(["ab", "cab", "abc", "zz"], "ab", ["abc"])).toEqual(["ab", "cab"]);
  });
});

describe("starsFromKey", () => {
  test("1-5 set and 0 clears; others are ignored", () => {
    for (const k of ["1", "2", "3", "4", "5"]) expect(starsFromKey(k)).toBe(Number(k));
    expect(starsFromKey("0")).toBeNull();
    expect(starsFromKey("6")).toBeUndefined();
    expect(starsFromKey("a")).toBeUndefined();
    expect(starsFromKey("Delete")).toBeNull();
  });
});
