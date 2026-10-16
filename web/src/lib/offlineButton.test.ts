import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import { createSession } from "../api/session";
import { addToOfflineSet, removeFromOfflineSet } from "./offlineButton";

const item = (id: string) => ({ track_id: id, position: 0, title: id, album: "", duration_s: 1, flac: null, mp3: null, lossy: false, status: "draft" });
const set = (revision: number, ids: string[]) => ({ playlist_id: "pl1", name: "Offline", revision, limit_items: 100, items: ids.map(item), totals: { items: ids.length, flac_bytes: 0, mp3_bytes: 0 }, updated_at: "" });
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json" } });

function rig(handler: (req: Request, body: unknown, n: number) => Response) {
  const log: { method: string; path: string; body: unknown }[] = [];
  const events: string[] = [];
  let n = 0;
  const api = createApi({
    baseUrl: "http://localhost",
    session: createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} }, sleep: async () => {}, onLine: () => true, fetch: async () => new Response("{}") }),
    fetch: async (req) => {
      const text = req.method === "GET" ? "" : await req.clone().text();
      const body = text ? JSON.parse(text) : undefined;
      log.push({ method: req.method, path: new URL(req.url).pathname, body });
      events.push(req.method + " " + new URL(req.url).pathname);
      return handler(req, body, n++);
    },
  });
  const deps = { download: async (id: string) => void events.push("download " + id), remove: async (id: string) => void events.push("evict " + id) };
  return { api, log, events, deps };
}

describe("addToOfflineSet", () => {
  test("update_playlist add_track_ids with the current revision, then download", async () => {
    const r = rig((req) => (req.method === "GET" ? json(set(7, ["a"])) : json({ id: "pl1", revision: 8 })));
    expect(await addToOfflineSet(r.api, "t1", r.deps)).toEqual({ ok: true });
    expect(r.log[1]).toEqual({ method: "PATCH", path: "/api/playlists/pl1", body: { revision: 7, add_track_ids: ["t1"] } });
    expect(r.events).toEqual(["GET /api/offline-set", "PATCH /api/playlists/pl1", "download t1"]);
  });

  test("409 REVISION_CONFLICT re-reads and retries once", async () => {
    let gets = 0;
    const r = rig((req, _b, n) => {
      if (req.method === "GET") return json(set(++gets === 1 ? 7 : 9, ["a"]));
      return n === 1 ? json({ code: "REVISION_CONFLICT", message: "stale", details: {} }, 409) : json({ id: "pl1", revision: 10 });
    });
    expect(await addToOfflineSet(r.api, "t1", r.deps)).toEqual({ ok: true });
    const patches = r.log.filter((l) => l.method === "PATCH");
    expect(patches.map((p) => (p.body as { revision: number }).revision)).toEqual([7, 9]);
    expect(r.events.at(-1)).toBe("download t1");
  });

  test("a second conflict gives up without downloading", async () => {
    const r = rig((req) => (req.method === "GET" ? json(set(7, [])) : json({ code: "REVISION_CONFLICT", message: "stale", details: {} }, 409)));
    const out = await addToOfflineSet(r.api, "t1", r.deps);
    expect(out.ok).toBe(false);
    expect(r.log.filter((l) => l.method === "PATCH")).toHaveLength(2);
    expect(r.events.some((e) => e.startsWith("download"))).toBe(false);
  });

  test("already in the set: no update, just download; set full: refused", async () => {
    const a = rig(() => json(set(7, ["t1"])));
    expect(await addToOfflineSet(a.api, "t1", a.deps)).toEqual({ ok: true });
    expect(a.log.some((l) => l.method === "PATCH")).toBe(false);
    expect(a.events.at(-1)).toBe("download t1");
    const full = { ...set(7, ["a"]), limit_items: 1 };
    const b = rig(() => json(full));
    const out = await addToOfflineSet(b.api, "t1", b.deps);
    expect(out.ok).toBe(false);
    expect(b.log.some((l) => l.method === "PATCH")).toBe(false);
  });
});

describe("removeFromOfflineSet", () => {
  test("track_ids without it, then evict", async () => {
    const r = rig((req) => (req.method === "GET" ? json(set(4, ["a", "t1", "b"])) : json({ id: "pl1", revision: 5 })));
    expect(await removeFromOfflineSet(r.api, "t1", r.deps)).toEqual({ ok: true });
    expect(r.log[1]!.body).toEqual({ revision: 4, track_ids: ["a", "b"] });
    expect(r.events.at(-1)).toBe("evict t1");
  });

  test("a conflict retries once with a fresh read", async () => {
    let gets = 0;
    const r = rig((req, _b, n) => {
      if (req.method === "GET") return json(gets++ === 0 ? set(4, ["t1"]) : set(6, ["t1", "c"]));
      return n === 1 ? json({ code: "REVISION_CONFLICT", message: "stale", details: {} }, 409) : json({ id: "pl1", revision: 7 });
    });
    expect(await removeFromOfflineSet(r.api, "t1", r.deps)).toEqual({ ok: true });
    expect(r.log.filter((l) => l.method === "PATCH").map((p) => p.body)).toEqual([{ revision: 4, track_ids: [] }, { revision: 6, track_ids: ["c"] }]);
    expect(r.events.at(-1)).toBe("evict t1");
  });
});
