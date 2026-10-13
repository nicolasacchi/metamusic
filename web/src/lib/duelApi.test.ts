import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import { createSession } from "../api/session";
import { createDuel, submitVerdict } from "./duelApi";
import { duelRevealed } from "./fixtures/m2b";

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
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

describe("duelApi", () => {
  test("createDuel sends the Idempotency-Key header and the body", async () => {
    const { api, seen } = fakeApi(() => json(201, duelRevealed));
    const d = await createDuel(api, "a", "b", "key-1", { blind: true });
    expect(d.id).toBe(duelRevealed.id);
    expect(seen[0]!.headers.get("Idempotency-Key")).toBe("key-1");
    expect(await seen[0]!.json()).toEqual({ a_track_id: "a", b_track_id: "b", blind: true });
  });
  test("a 5xx becomes an HttpError carrying the status", async () => {
    const { api } = fakeApi(() => json(503, { code: "UNAVAILABLE", message: "clips" }));
    const e = await createDuel(api, "a", "b", "k", { blind: false }).catch((x: unknown) => x);
    expect((e as { status: number }).status).toBe(503);
  });
  test("submitVerdict returns the duel of the result", async () => {
    const { api, seen } = fakeApi(() => json(201, { duel: duelRevealed, replayed: false, verdict: duelRevealed.human_verdict }));
    const d = await submitVerdict(api, "id1", { verdict: "a", note: "", latency_ms: 0, switches: 2 });
    expect(d.id).toBe(duelRevealed.id);
    expect(new URL(seen[0]!.url).pathname).toBe("/api/duels/id1/verdicts");
    expect(await seen[0]!.json()).toEqual({ verdict: "a", switches: 2 });
  });
});
