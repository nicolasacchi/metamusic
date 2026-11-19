import { afterEach, expect, test } from "bun:test";
import { cacheStore, DB_NAME } from "./store";
import { dropOfflineDb } from "../browserState";

// A minimal IndexedDB double that models the one behaviour the reset depends on: deleteDatabase fires
// versionchange on every open connection and is BLOCKED (not deleted) while any of them stays open.
function fakeIdb() {
  const conns: { closed: boolean; onversionchange: null | (() => void) }[] = [];
  const state = { deleted: false, blocked: false };
  const idb = {
    open(_name: string, _v: number) {
      const r: Record<string, unknown> = {};
      const conn = {
        closed: false,
        onversionchange: null as null | (() => void),
        close() {
          conn.closed = true;
        },
        createObjectStore() {},
        transaction() {
          return {
            objectStore: () => ({
              getAll() {
                const q: Record<string, unknown> = { result: [] };
                queueMicrotask(() => (q.onsuccess as () => void)?.());
                return q;
              },
            }),
          };
        },
      };
      conns.push(conn);
      r.result = conn;
      queueMicrotask(() => {
        (r.onupgradeneeded as () => void)?.();
        (r.onsuccess as () => void)?.();
      });
      return r;
    },
    deleteDatabase(name: string) {
      const r: Record<string, unknown> = {};
      queueMicrotask(() => {
        expect(name).toBe(DB_NAME);
        for (const c of conns) if (!c.closed) c.onversionchange?.();
        if (conns.some((c) => !c.closed)) {
          state.blocked = true;
          (r.onblocked as () => void)?.();
        } else {
          state.deleted = true;
          (r.onsuccess as () => void)?.();
        }
      });
      return r;
    },
  };
  return { idb, conns, state };
}

const realIdb = (globalThis as { indexedDB?: unknown }).indexedDB;
afterEach(() => {
  (globalThis as { indexedDB?: unknown }).indexedDB = realIdb;
});

test("per-user reset: the open mm-offline connection closes on versionchange so deleteDatabase is not blocked", async () => {
  const { idb, conns, state } = fakeIdb();
  (globalThis as { indexedDB?: unknown }).indexedDB = idb;
  const store = cacheStore({ caches: undefined as never, indexedDB: idb as never });
  await store.listFiles(); // opens and keeps the connection
  expect(conns.length).toBe(1);
  expect(conns[0]!.closed).toBe(false);

  await dropOfflineDb();
  expect(state.blocked).toBe(false);
  expect(state.deleted).toBe(true);
  expect(conns[0]!.closed).toBe(true);

  // The cached open promise was cleared: the next use reopens instead of using the closed handle.
  await store.listFiles();
  expect(conns.length).toBe(2);
  expect(conns[1]!.closed).toBe(false);
});
