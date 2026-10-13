import { describe, expect, test } from "bun:test";
import type { components } from "./schema";
import { createOutbox, memoryStore, type PostResult } from "./outbox";

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
