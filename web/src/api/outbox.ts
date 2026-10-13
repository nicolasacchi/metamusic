// Listen-event outbox (C40.3): IndexedDB store (in-memory fallback), flush through the generated
// client, sendBeacon / keepalive-fetch on page hide. Lives in src/api/ because it is network code.
import { api } from "./client";
import { RateLimited } from "./client";
import type { components } from "./schema";
import { uuidv7 } from "../lib/id";

export type ListenEvent = components["schemas"]["ListenEventIn"];

export const CAP = 5000;
const FLUSH_AT = 10;
const BATCH = 100;
const INTERVAL_MS = 30_000;
const DEFAULT_RETRY_S = 30;
const BEACON_URL = "/api/listen-events?mm_client=spa";

export function newEventId(): string {
  return uuidv7();
}

interface Stored {
  event: ListenEvent;
  beaconed: boolean;
}

export interface Store {
  /** Oldest first (ids are UUIDv7, so key order is time order). */
  all(): Promise<Stored[]>;
  put(rows: Stored[]): Promise<void>;
  remove(ids: string[]): Promise<void>;
}

export function memoryStore(): Store {
  const rows = new Map<string, Stored>();
  return {
    async all() {
      return [...rows.values()].sort((a, b) => (a.event.id < b.event.id ? -1 : 1)).map((r) => ({ ...r }));
    },
    async put(rs) {
      for (const r of rs) rows.set(r.event.id, { ...r });
    },
    async remove(ids) {
      for (const id of ids) rows.delete(id);
    },
  };
}

function req<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((res, rej) => {
    r.onsuccess = () => res(r.result);
    r.onerror = () => rej(r.error);
  });
}

/** IndexedDB-backed store; rejects at open time when IndexedDB is unusable. */
export async function idbStore(): Promise<Store> {
  const db = await new Promise<IDBDatabase>((res, rej) => {
    const o = indexedDB.open("mm-outbox", 1);
    o.onupgradeneeded = () => o.result.createObjectStore("events", { keyPath: "event.id" });
    o.onsuccess = () => res(o.result);
    o.onerror = () => rej(o.error);
    o.onblocked = () => rej(new Error("blocked"));
  });
  const tx = (mode: IDBTransactionMode) => db.transaction("events", mode).objectStore("events");
  return {
    async all() {
      return (await req(tx("readonly").getAll())) as Stored[];
    },
    async put(rows) {
      const s = tx("readwrite");
      for (const r of rows) s.put(r);
      await new Promise<void>((res, rej) => {
        s.transaction.oncomplete = () => res();
        s.transaction.onerror = () => rej(s.transaction.error);
        s.transaction.onabort = () => rej(s.transaction.error);
      });
    },
    async remove(ids) {
      const s = tx("readwrite");
      for (const id of ids) s.delete(id);
      await new Promise<void>((res, rej) => {
        s.transaction.oncomplete = () => res();
        s.transaction.onerror = () => rej(s.transaction.error);
        s.transaction.onabort = () => rej(s.transaction.error);
      });
    },
  };
}

/** Lazy store: IndexedDB when it works, otherwise memory for the life of the page. */
function lazyStore(): Store {
  let p: Promise<Store> | null = null;
  const get = () =>
    (p ??= (async () => {
      try {
        return await idbStore();
      } catch {
        return memoryStore();
      }
    })());
  return {
    all: async () => (await get()).all(),
    put: async (r) => (await get()).put(r),
    remove: async (i) => (await get()).remove(i),
  };
}

export type PostResult =
  | { kind: "ok" }
  | { kind: "retry" }
  | { kind: "drop" }
  | { kind: "rate"; retryAfterS: number | null };

export interface OutboxDeps {
  store: Store;
  post(events: ListenEvent[]): Promise<PostResult>;
  /** navigator.sendBeacon; may throw or return false. */
  beacon(json: string): boolean;
  /** fetch(..., {keepalive: true}) fallback; fire and forget. */
  keepalive(json: string): void;
  now(): number;
}

export function createOutbox(deps: OutboxDeps) {
  let blockedUntil = 0;
  let flushing: Promise<void> | null = null;
  // Serialises store mutations so concurrent appends cannot interleave with the cap trim.
  let chain: Promise<unknown> = Promise.resolve();
  const serial = <T>(fn: () => Promise<T>): Promise<T> => {
    const r = chain.then(fn, fn);
    chain = r.catch(() => {});
    return r;
  };

  const box = {
    dropped: 0,

    async pending(): Promise<number> {
      return (await deps.store.all()).length;
    },

    async snapshot(): Promise<ListenEvent[]> {
      return (await deps.store.all()).map((r) => r.event);
    },

    appendMany(events: ListenEvent[]): Promise<void> {
      return serial(async () => {
        await deps.store.put(events.map((event) => ({ event, beaconed: false })));
        const all = await deps.store.all();
        if (all.length > CAP) {
          const drop = all.slice(0, all.length - CAP).map((r) => r.event.id);
          await deps.store.remove(drop);
          box.dropped += drop.length;
        }
        if (all.length >= FLUSH_AT) void box.flush();
      });
    },

    append(e: ListenEvent): Promise<void> {
      return box.appendMany([e]);
    },

    /** Resolves when any running flush is finished. */
    async idle(): Promise<void> {
      await flushing;
      await chain;
    },

    flush(): Promise<void> {
      if (flushing) return flushing;
      if (deps.now() < blockedUntil) return Promise.resolve();
      flushing = (async () => {
        try {
          for (;;) {
            const rows = (await deps.store.all()).slice(0, BATCH);
            if (rows.length === 0) return;
            let r: PostResult;
            try {
              r = await deps.post(rows.map((x) => x.event));
            } catch {
              return;
            }
            if (r.kind === "rate") {
              blockedUntil = deps.now() + (r.retryAfterS ?? DEFAULT_RETRY_S) * 1000;
              return;
            }
            if (r.kind === "retry") return;
            // ok: accepted, duplicate and rejected ids are all settled server-side; drop: never acceptable.
            await serial(() => deps.store.remove(rows.map((x) => x.event.id)));
            if (rows.length < BATCH) return;
          }
        } finally {
          flushing = null;
        }
      })();
      return flushing;
    },

    /** pagehide / visibilitychange hidden: beacon what is not yet beaconed and mark it. */
    onHide(): Promise<void> {
      return serial(async () => {
        const rows = (await deps.store.all()).filter((r) => !r.beaconed);
        for (let i = 0; i < rows.length; i += BATCH) {
          const chunk = rows.slice(i, i + BATCH);
          const json = JSON.stringify({ events: chunk.map((r) => r.event) });
          let ok: boolean;
          try {
            ok = deps.beacon(json);
          } catch {
            ok = false;
          }
          if (!ok) {
            try {
              deps.keepalive(json);
            } catch {
              /* nothing more to try; the next load flushes */
            }
          }
          await deps.store.put(chunk.map((r) => ({ ...r, beaconed: true })));
        }
      });
    },
  };
  return box;
}

export type Outbox = ReturnType<typeof createOutbox>;

async function postViaClient(events: ListenEvent[]): Promise<PostResult> {
  try {
    const r = await api.POST("/api/listen-events", { body: { events } });
    if (r.data) return { kind: "ok" };
    const s = r.response.status;
    if (s === 400 || s === 413 || s === 422) return { kind: "drop" };
    return { kind: "retry" };
  } catch (e) {
    if (e instanceof RateLimited) return { kind: "rate", retryAfterS: e.retryAfterS };
    return { kind: "retry" };
  }
}

export const outbox: Outbox = createOutbox({
  store: lazyStore(),
  post: postViaClient,
  beacon: (json) => navigator.sendBeacon(BEACON_URL, new Blob([json], { type: "application/json" })),
  keepalive: (json) => {
    void fetch(BEACON_URL, {
      method: "POST",
      body: json,
      keepalive: true,
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-MM-Client": "spa", "X-Requested-With": "XMLHttpRequest" },
    }).catch(() => {});
  },
  now: () => Date.now(),
});

/** Flush on load, every 30 s, on `online`; beacon on pagehide / hidden. Returns the uninstaller. */
export function startOutbox(box: Outbox = outbox): () => void {
  const flush = () => void box.flush();
  const hide = () => void box.onHide();
  const vis = () => {
    if (document.visibilityState === "hidden") hide();
  };
  window.addEventListener("online", flush);
  window.addEventListener("pagehide", hide);
  document.addEventListener("visibilitychange", vis);
  const h = setInterval(flush, INTERVAL_MS);
  flush();
  return () => {
    clearInterval(h);
    window.removeEventListener("online", flush);
    window.removeEventListener("pagehide", hide);
    document.removeEventListener("visibilitychange", vis);
  };
}
