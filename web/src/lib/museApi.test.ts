import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import { createApi } from "../api/client";
import type { createSession } from "../api/session";
import { assignSongs, createMuse, updateSession } from "./museApi";

function fakeApi(handler: (req: Request) => Response) {
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
const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json" } });
const muse: components["schemas"]["Muse"] = { id: "m1", kind: "reference", title: "Synthetic", artist: null, brief: null, source: "human", created_by: "u", updated_at: "x", revision: 1, hidden: false };

describe("museApi", () => {
  test("create_muse sends an Idempotency-Key, the same one on a retry, a new one for a new action", async () => {
    const { api, seen } = fakeApi(() => json(201, muse));
    const act = createMuse(api, { kind: "reference", title: "Synthetic" });
    const r1 = await act();
    await act();
    expect(r1).toEqual({ kind: "created", muse });
    const k1 = seen[0]!.headers.get("Idempotency-Key");
    expect(k1).toBeTruthy();
    expect(seen[1]!.headers.get("Idempotency-Key")).toBe(k1!);
    await createMuse(api, { kind: "reference", title: "Synthetic" })();
    expect(seen[2]!.headers.get("Idempotency-Key")).not.toBe(k1!);
    expect(await seen[0]!.json()).toEqual({ kind: "reference", title: "Synthetic" });
  });
  test("409 MUSE_EXISTS maps to the existing Muse (read through get_muse)", async () => {
    const { api, seen } = fakeApi((req) => {
      const p = new URL(req.url).pathname;
      if (req.method === "POST") return json(409, { code: "MUSE_EXISTS", message: "exists", details: { muse_id: "m1", hidden: false } });
      expect(p).toBe("/api/muses/m1");
      return json(200, { muse: muse, coverage: {}, angles: [], songs: [], songs_total: 0 });
    });
    const r = await createMuse(api, { kind: "reference", title: "Synthetic" })();
    expect(r).toEqual({ kind: "existing", muse, hidden: false });
    expect(seen).toHaveLength(2);
  });
  test("other failures and network errors are values, not throws", async () => {
    const { api } = fakeApi(() => json(422, { code: "VALIDATION_FAILED", message: "bad", details: {} }));
    expect(await createMuse(api, { kind: "brief", title: "" })()).toMatchObject({ kind: "failed", status: 422, code: "VALIDATION_FAILED" });
    const net = createApi({
      baseUrl: "http://x.test",
      fetch: () => Promise.reject(new Error("down")),
      session: { ok() {}, confirmed() {}, networkFailure: () => Promise.resolve(), retryDelay: () => Promise.resolve() } as unknown as ReturnType<typeof createSession>,
    });
    expect(await createMuse(net, { kind: "brief", title: "x" })()).toEqual({ kind: "network" });
  });
  test("assignSongs posts the ids to the Muse", async () => {
    const { api, seen } = fakeApi(() => json(200, { assigned: 2, unchanged: 0 }));
    const r = await assignSongs(api, "m1", ["a", "b"]);
    expect(r).toEqual({ kind: "ok", value: { assigned: 2, unchanged: 0 } });
    expect(new URL(seen[0]!.url).pathname).toBe("/api/muses/m1/songs");
    expect(await seen[0]!.json()).toEqual({ song_ids: ["a", "b"] });
  });
  test("updateSession sends the revision and reports a conflict", async () => {
    const { api, seen } = fakeApi(() => json(409, { code: "REVISION_CONFLICT", message: "stale", details: {} }));
    const r = await updateSession(api, "s1", { revision: 3, name: "x" });
    expect(r).toMatchObject({ kind: "failed", status: 409, code: "REVISION_CONFLICT" });
    expect(seen[0]!.method).toBe("PATCH");
    expect(await seen[0]!.json()).toEqual({ revision: 3, name: "x" });
  });
});
