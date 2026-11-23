import { afterEach, beforeEach, expect, test } from "bun:test";
import { IDBFactory } from "fake-indexeddb";
import { idbStore, type ListenEvent } from "./outbox";
import { OUTBOX_DB } from "../lib/me";

const real = globalThis.indexedDB;
beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
});
afterEach(() => {
  globalThis.indexedDB = real;
});

const ev = (n: number): ListenEvent => ({
  id: `0190000${String(n).padStart(5, "0")}-0000-7000-8000-000000000000`,
  track_id: "t1",
  session_id: "s1",
  kind: "segment",
  from_ms: 0,
  to_ms: 1000,
  client_at: "2026-01-01T00:00:00Z",
});

/** What a phone running the pre-Morning-Crate build left behind: version 1, one "events" store, queued rows. */
function seedV1(rows: object[]): Promise<void> {
  return new Promise((res, rej) => {
    const o = indexedDB.open(OUTBOX_DB, 1);
    o.onupgradeneeded = () => o.result.createObjectStore("events", { keyPath: "event.id" });
    o.onerror = () => rej(o.error);
    o.onsuccess = () => {
      const db = o.result;
      const tx = db.transaction("events", "readwrite");
      for (const r of rows) tx.objectStore("events").put(r);
      tx.oncomplete = () => {
        db.close();
        res();
      };
      tx.onerror = () => rej(tx.error);
    };
  });
}

test("upgrading a v1 outbox to v2 keeps the queued listen events and adds the morning store", async () => {
  const queued = [ev(1), ev(2), ev(3)].map((event) => ({ event, tries: 1, next_at: 0 }));
  await seedV1(queued);

  const events = await idbStore("events");
  const got = await events.all();
  expect(got.map((r) => r.event.id).sort()).toEqual(queued.map((r) => r.event.id));

  // The new channel exists and starts empty, and shares the same database.
  const morning = await idbStore("morning");
  expect(await morning.all()).toEqual([]);

  // Flushing (removing) the old rows works after the upgrade.
  await events.remove([queued[0]!.event.id, queued[1]!.event.id]);
  expect((await events.all()).map((r) => r.event.id)).toEqual([queued[2]!.event.id]);
});

test("the v1 queue is flushed through the app's outbox after the upgrade", async () => {
  const { createOutbox } = await import("./outbox");
  const queued = [ev(10), ev(11)].map((event) => ({ event, tries: 0, next_at: 0 }));
  await seedV1(queued);

  const store = await idbStore("events");
  const sent: string[] = [];
  const box = createOutbox({
    store,
    post: async (es: ListenEvent[]) => {
      sent.push(...es.map((e) => e.id));
      return { kind: "ok" };
    },
    beacon: () => false,
    keepalive: () => {},
    now: () => 1_000_000,
  });
  await box.flush();
  expect(sent.sort()).toEqual(queued.map((r) => r.event.id));
  expect(await store.all()).toEqual([]);
});
