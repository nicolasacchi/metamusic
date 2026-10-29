import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import type { createSession } from "../api/session";
import { addMembers, addSongsToRecord, capForRecord, countMatches, createCrate, failure, itemsText, updateCrate } from "./crateApi";

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
const detail = { crate: { id: "c1" }, count: 0, warnings: [] };

describe("crateApi", () => {
  test("createCrate: one key per action, reused on retry; smart carries the filter", async () => {
    const { api, seen } = fake(() => json(201, detail));
    const act = createCrate(api, { name: "n", kind: "smart", subject: "song", filter: { unheard: true } });
    await act();
    await act();
    expect(seen[0]!.headers.get("Idempotency-Key")).toBe(seen[1]!.headers.get("Idempotency-Key"));
    expect(await seen[0]!.json()).toEqual({ name: "n", kind: "smart", subject: "song", filter: { unheard: true } });
  });
  test("updateCrate sends the revision; STALE_REVISION is flagged", async () => {
    const { api, seen } = fake(() => json(409, { code: "STALE_REVISION", message: "" }));
    const r = await updateCrate(api, "c1", 4, { hidden: true });
    expect(await seen[0]!.json()).toEqual({ revision: 4, hidden: true });
    expect(r.ok).toBe(false);
    if (!r.ok) {
      expect(r.stale).toBe(true);
      expect(r.message).toContain("Changed elsewhere");
    }
  });
  test("addMembers chunks by 200 and sums", async () => {
    const { api, seen } = fake(() => json(200, { added: 200, already: 0, removed: 0, not_member: 0, count: 200 }));
    const r = await addMembers(api, "c1", "take", Array.from({ length: 250 }, (_, i) => `t${i}`));
    expect(seen.length).toBe(2);
    const body = (await seen[0]!.json()) as { add: { track_id: string }[] };
    expect(body.add.length).toBe(200);
    expect(body.add[0]).toEqual({ track_id: "t0" });
    expect(r.ok && r.value.added).toBe(400);
  });
  test("countMatches asks the list op with limit 1 and reads total", async () => {
    const { api, seen } = fake(() => json(200, { items: [], next_cursor: null, total: 2 }));
    const r = await countMatches(api, "song", { sound: ["genre_family:reggae"], unheard: true });
    const u = new URL(seen[0]!.url);
    expect(u.pathname).toBe("/api/songs");
    expect(u.searchParams.get("limit")).toBe("1");
    expect(u.searchParams.getAll("sound")).toEqual(["genre_family:reggae"]);
    expect(r).toEqual({ ok: true, value: 2 });
  });
  test("addSongsToRecord retries once after a stale revision, with the re-read one", async () => {
    let posts = 0;
    const { api, seen } = fake((req) => {
      if (req.method === "GET") return json(200, { record: { revision: 9 } });
      posts++;
      return posts === 1 ? json(409, { code: "STALE_REVISION", message: "x" }) : json(200, { record: { revision: 10 } });
    });
    const r = await addSongsToRecord(api, { id: "r1", revision: 3 }, ["s1"], "t1");
    expect(r).toEqual({ ok: true, value: { added: 1, revision: 10 } });
    const last = (await seen.at(-1)!.json()) as { revision: number; ops: unknown[] };
    expect(last.revision).toBe(9);
    expect(last.ops).toEqual([{ op: "add", song_id: "s1", take_id: "t1" }]);
  });
  test("small helpers", () => {
    expect(capForRecord(["a", "a", "b"]).ids).toEqual(["a", "b"]);
    expect(itemsText({ added: 2, already: 1, removed: 0, not_member: 0, count: 3 }, "X")).toBe("Added 2, 1 already in it to X.");
    expect(failure(409, { code: "CRATE_FULL", message: "" }, "f").message).toContain("full");
  });
});
