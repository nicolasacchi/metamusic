import { describe, expect, test } from "bun:test";
import type { components } from "./schema";
import { createApi } from "./client";
import { createOutbox, memoryStore, morningBatches, morningBeaconUrl, onMorningResult, postMorning, type MorningOutEvent, type PostResult } from "./outbox";

type Ev = components["schemas"]["ListenEventIn"];

let n = 0;
function ev(over: Partial<Ev> = {}): Ev {
  n++;
  return {
    id: `0190000${String(n).padStart(5, "0")}-0000-7000-8000-000000000000`,
    track_id: "t1",
    session_id: "s1",
    kind: "segment",
    from_ms: 0,
    to_ms: 1000,
    client_at: "2026-01-01T00:00:00Z",
    ...over,
  };
}

function setup(opts: { post?: (e: Ev[]) => Promise<PostResult>; beacon?: (json: string) => boolean; keepalive?: (json: string) => void } = {}) {
  let now = 1_000_000;
  const sent: Ev[][] = [];
  const beacons: string[] = [];
  const keep: string[] = [];
  const store = memoryStore();
  const box = createOutbox({
    store,
    post:
      opts.post ??
      (async (e) => {
        sent.push(e);
        return { kind: "ok" };
      }),
    beacon:
      opts.beacon ??
      ((j) => {
        beacons.push(j);
        return true;
      }),
    keepalive: opts.keepalive ?? ((j) => void keep.push(j)),
    now: () => now,
  });
  return { box, store, sent, beacons, keep, advance: (ms: number) => (now += ms) };
}

describe("outbox", () => {
  test("append stores events; no flush below 10", async () => {
    const { box, sent } = setup();
    for (let i = 0; i < 9; i++) await box.append(ev());
    expect(await box.pending()).toBe(9);
    expect(sent).toHaveLength(0);
  });

  test("flushes at 10 pending and deletes everything the server answered for", async () => {
    const { box, sent } = setup();
    for (let i = 0; i < 10; i++) await box.append(ev());
    await box.idle();
    expect(sent).toHaveLength(1);
    expect(sent[0]).toHaveLength(10);
    expect(await box.pending()).toBe(0);
  });

  test("a rejected-only or duplicate answer still deletes (server decided)", async () => {
    const { box } = setup({ post: async () => ({ kind: "ok" }) });
    await box.append(ev());
    await box.flush();
    expect(await box.pending()).toBe(0);
  });

  test("network error keeps events", async () => {
    const { box } = setup({ post: async () => ({ kind: "retry" }) });
    await box.append(ev());
    await box.flush();
    expect(await box.pending()).toBe(1);
  });

  test("a batch the server can never accept (400/422) is dropped, not retried forever", async () => {
    const { box } = setup({ post: async () => ({ kind: "drop" }) });
    await box.append(ev());
    await box.flush();
    expect(await box.pending()).toBe(0);
  });

  test("429 keeps every event and blocks the next flush until Retry-After", async () => {
    let calls = 0;
    const { box, advance } = setup({
      post: async () => {
        calls++;
        return calls === 1 ? { kind: "rate", retryAfterS: 12 } : { kind: "ok" };
      },
    });
    await box.append(ev());
    await box.flush();
    expect(calls).toBe(1);
    expect(await box.pending()).toBe(1);
    advance(11_999);
    await box.flush();
    expect(calls).toBe(1);
    advance(2);
    await box.flush();
    expect(calls).toBe(2);
    expect(await box.pending()).toBe(0);
  });

  test("429 without Retry-After waits 30 s", async () => {
    let calls = 0;
    const { box, advance } = setup({
      post: async () => {
        calls++;
        return { kind: "rate", retryAfterS: null };
      },
    });
    await box.append(ev());
    await box.flush();
    advance(29_999);
    await box.flush();
    expect(calls).toBe(1);
    advance(2);
    await box.flush();
    expect(calls).toBe(2);
  });

  test("cap 5000 drops the oldest and counts", async () => {
    const { box } = setup({ post: async () => ({ kind: "retry" }) });
    const evs = Array.from({ length: 5003 }, () => ev());
    await box.appendMany(evs);
    expect(await box.pending()).toBe(5000);
    expect(box.dropped).toBe(3);
    const ids = (await box.snapshot()).map((e) => e.id);
    expect(ids).not.toContain(evs[0]!.id);
    expect(ids).toContain(evs[5002]!.id);
  });

  test("hide beacons pending events, marks them, and a later flush re-sends then deletes", async () => {
    const { box, beacons, sent } = setup({});
    await box.append(ev());
    await box.append(ev());
    await box.onHide();
    expect(beacons).toHaveLength(1);
    expect(JSON.parse(beacons[0]!).events).toHaveLength(2);
    expect(await box.pending()).toBe(2);
    await box.onHide();
    expect(beacons).toHaveLength(1); // already beaconed: not re-beaconed
    await box.flush();
    expect(sent[0]).toHaveLength(2);
    expect(await box.pending()).toBe(0);
  });

  test("hide beacons synchronously: events appended this page life need no storage round trip", async () => {
    const { box, beacons } = setup({});
    void box.append(ev()); // not awaited: still queued behind the store write
    void box.append(ev());
    const done = box.onHide();
    // a pagehide handler gets no further tick: the beacon must already be out
    expect(beacons).toHaveLength(1);
    expect(JSON.parse(beacons[0]!).events).toHaveLength(2);
    await done;
    expect(beacons).toHaveLength(1); // not sent a second time from storage
    expect(await box.pending()).toBe(2); // kept (beaconed) until a flush is acknowledged
  });

  test("events delivered by a flush are not beaconed on hide", async () => {
    const { box, beacons } = setup({});
    await box.append(ev());
    await box.flush();
    await box.onHide();
    expect(beacons).toHaveLength(0);
  });

  test("sendBeacon throwing or returning false falls back to keepalive fetch", async () => {
    const a = setup({
      beacon: () => {
        throw new Error("boom");
      },
    });
    await a.box.append(ev());
    await a.box.onHide();
    expect(a.keep).toHaveLength(1);
    const b = setup({ beacon: () => false });
    await b.box.append(ev());
    await b.box.onHide();
    expect(b.keep).toHaveLength(1);
    expect(await b.box.pending()).toBe(1);
  });

  test("ids and session ids produced by newEvent are uuidv7", async () => {
    const { newEventId } = await import("./outbox");
    expect(newEventId()).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });
});

describe("morning channel (C384.4)", () => {
  const mev = (crate: string, over: Partial<MorningOutEvent> = {}): MorningOutEvent => {
    n++;
    return { crate_id: crate, id: `0190000${String(n).padStart(5, "0")}-0000-7000-8000-000000000001`, side: 0, chapter: 1, kind: "skip", side_ms: 5000, client_at: "2026-11-23T08:00:00Z", ...over };
  };

  function rig(post: (e: MorningOutEvent[]) => Promise<PostResult>) {
    const beacons: { url: string; json: string }[] = [];
    const box = createOutbox<MorningOutEvent>({
      store: memoryStore<MorningOutEvent>(),
      post,
      flushAt: 1,
      batches: morningBatches,
      body: (b) => JSON.stringify({ events: b.map(({ crate_id: _c, ...e }) => (void _c, e)) }),
      beacon: (json, b) => (beacons.push({ url: morningBeaconUrl(b[0]!.crate_id), json }), true),
      keepalive: () => {},
      now: () => 1_000_000,
    });
    return { box, beacons };
  }

  test("batches never mix crates and stay under 100", () => {
    const evs = [mev("a"), mev("b"), mev("a"), ...Array.from({ length: 101 }, () => mev("c"))];
    const b = morningBatches(evs);
    expect(b.map((x) => [x[0]!.crate_id, x.length])).toEqual([["a", 2], ["b", 1], ["c", 100], ["c", 1]]);
  });

  test("an event is sent as soon as it is queued, and once it settles it is never sent again", async () => {
    const sent: MorningOutEvent[][] = [];
    const { box } = rig(async (e) => (sent.push(e), { kind: "ok" }));
    const e = mev("a");
    await box.append(e);
    await box.idle();
    expect(sent).toEqual([[e]]);
    expect(await box.pending()).toBe(0);
    await box.flush();
    expect(sent).toHaveLength(1);
  });

  test("a failed send keeps the SAME event id for the retry (the server counts a replay as a duplicate)", async () => {
    const seen: string[] = [];
    let fail = true;
    const { box } = rig(async (e) => {
      seen.push(...e.map((x) => x.id));
      return fail ? { kind: "retry" } : { kind: "ok" };
    });
    await box.append(mev("a"));
    await box.idle();
    fail = false;
    await box.flush();
    expect(seen).toHaveLength(2);
    expect(seen[0]).toBe(seen[1]!);
    expect(await box.pending()).toBe(0);
  });

  test("hiding the page beacons each crate to its own URL without the crate id in the body, once", async () => {
    const { box, beacons } = rig(async () => ({ kind: "retry" }));
    await box.append(mev("a"));
    await box.append(mev("b"));
    await box.onHide();
    await box.onHide();
    expect(beacons.map((b) => b.url)).toEqual(["/api/morning-crates/a/events?mm_client=spa", "/api/morning-crates/b/events?mm_client=spa"]);
    expect(beacons[0]!.json).not.toContain("crate_id");
    expect(JSON.parse(beacons[0]!.json).events).toHaveLength(1);
  });

  test("the listen channel is unaffected by a morning box (separate stores)", async () => {
    const { box } = rig(async () => ({ kind: "retry" }));
    const l = setup();
    await box.append(mev("a"));
    expect(await l.box.pending()).toBe(0);
    expect(await box.pending()).toBe(1);
  });
});

describe("postMorning", () => {
  const api = (status: number, body: unknown) =>
    createApi({ baseUrl: "http://x.test", fetch: async () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": status < 300 ? "application/json" : "application/problem+json" } }) });
  const e: MorningOutEvent = { crate_id: "a", id: "i1", side: 0, chapter: 1, kind: "rate", side_ms: 0, client_at: "2026-11-23T08:00:00Z", stars: 4 };

  test("accepted: ok, listeners hear revealed, the body carries no crate id", async () => {
    let body = "";
    const client = createApi({
      baseUrl: "http://x.test",
      fetch: async (r) => {
        body = await r.text();
        return new Response(JSON.stringify({ accepted: 1, duplicates: 0, rejected: [], revealed: true }), { status: 200, headers: { "Content-Type": "application/json" } });
      },
    });
    const heard: unknown[] = [];
    const off = onMorningResult((r) => heard.push(r));
    expect(await postMorning([e], client)).toEqual({ kind: "ok" });
    off();
    expect(heard).toEqual([{ crate_id: "a", revealed: true }]);
    expect(JSON.parse(body)).toEqual({ events: [{ id: "i1", side: 0, chapter: 1, kind: "rate", side_ms: 0, client_at: "2026-11-23T08:00:00Z", stars: 4 }] });
  });

  test("never-acceptable answers drop, server trouble retries", async () => {
    for (const s of [400, 404, 409, 413, 422]) expect(await postMorning([e], api(s, { code: "X", message: "m" }))).toEqual({ kind: "drop" });
    for (const c of ["HUMAN_ONLY", "FORBIDDEN_ROLE"]) expect(await postMorning([e], api(403, { code: c, message: "m" }))).toEqual({ kind: "drop" });
    // an edge or session 403 is transient: the blind ratings stay queued
    expect(await postMorning([e], api(403, { code: "FORBIDDEN", message: "m" }))).toEqual({ kind: "retry" });
    expect(await postMorning([e], createApi({ baseUrl: "http://x.test", fetch: async () => new Response("denied", { status: 403 }) }))).toEqual({ kind: "retry" });
    for (const s of [500, 502, 503]) expect(await postMorning([e], api(s, { code: "X", message: "m" }))).toEqual({ kind: "retry" });
  });

  test("one rejected event does not discard the valid ones of its batch", async () => {
    const seen: string[][] = [];
    const client = createApi({
      baseUrl: "http://x.test",
      fetch: async (r) => {
        const ids = ((await r.json()) as { events: { id: string }[] }).events.map((x) => x.id);
        seen.push(ids);
        if (ids.includes("bad")) return new Response(JSON.stringify({ code: "VALIDATION", message: "m" }), { status: 422, headers: { "Content-Type": "application/problem+json" } });
        return new Response(JSON.stringify({ accepted: 1, duplicates: 0, rejected: [], revealed: false }), { status: 200, headers: { "Content-Type": "application/json" } });
      },
    });
    const r = await postMorning([{ ...e, id: "g1" }, { ...e, id: "bad" }, { ...e, id: "g2" }], client);
    expect(r).toEqual({ kind: "drop" });
    expect(seen).toEqual([["g1", "bad", "g2"], ["g1"], ["bad"], ["g2"]]);
  });

  test("429 is a rate answer with Retry-After", async () => {
    const client = createApi({ baseUrl: "http://x.test", fetch: async () => new Response("{}", { status: 429, headers: { "Retry-After": "7" } }) });
    expect(await postMorning([e], client)).toEqual({ kind: "rate", retryAfterS: 7 });
  });

  test("a network failure retries", async () => {
    const client = createApi({ baseUrl: "http://x.test", fetch: async () => Promise.reject(new TypeError("offline")), session: { retryDelay: async () => {}, networkFailure: async () => {}, ok() {}, confirmed() {} } as never });
    expect(await postMorning([e], client)).toEqual({ kind: "retry" });
  });

  test("one crate that must retry keeps the whole batch (replays are duplicates)", async () => {
    let call = 0;
    const client = createApi({
      baseUrl: "http://x.test",
      fetch: async () => (++call === 1 ? new Response(JSON.stringify({ accepted: 1, duplicates: 0, rejected: [], revealed: false }), { status: 200, headers: { "Content-Type": "application/json" } }) : new Response("{}", { status: 503 })),
    });
    expect(await postMorning([e, { ...e, crate_id: "b", id: "i2" }], client)).toEqual({ kind: "retry" });
  });
});
