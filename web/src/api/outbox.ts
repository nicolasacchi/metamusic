// Listen-event outbox (C40.3): IndexedDB store (in-memory fallback), flush through the generated
// client, sendBeacon / keepalive-fetch on page hide. Lives in src/api/ because it is network code.
import { api } from "./client";
import { RateLimited } from "./client";
import type { components } from "./schema";
import { uuidv7 } from "../lib/id";
import { OUTBOX_DB } from "../lib/me";

export type ListenEvent = components["schemas"]["ListenEventIn"];
/** A Morning Crate event waiting to be sent (C384.4): the endpoint is per crate, so the crate id travels with it. */
export type MorningOutEvent = components["schemas"]["MorningEvent"] & { crate_id: string };

export const CAP = 5000;
const FLUSH_AT = 10;
const BATCH = 100;
const INTERVAL_MS = 30_000;
const DEFAULT_RETRY_S = 30;
const BEACON_URL = "/api/listen-events?mm_client=spa";

export function newEventId(): string {
  return uuidv7();
}

interface Stored<E extends { id: string } = ListenEvent> {
  event: E;
  beaconed: boolean;
}

export interface Store<E extends { id: string } = ListenEvent> {
  /** Oldest first (ids are UUIDv7, so key order is time order). */
  all(): Promise<Stored<E>[]>;
  put(rows: Stored<E>[]): Promise<void>;
  remove(ids: string[]): Promise<void>;
}

export function memoryStore<E extends { id: string } = ListenEvent>(): Store<E> {
  const rows = new Map<string, Stored<E>>();
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

/** The object stores of the one outbox database: the listen events and the Morning Crate events (version 2). */
export type Channel = "events" | "morning";

/** IndexedDB-backed store; rejects at open time when IndexedDB is unusable. */
export async function idbStore<E extends { id: string } = ListenEvent>(channel: Channel = "events"): Promise<Store<E>> {
  const db = await new Promise<IDBDatabase>((res, rej) => {
    const o = indexedDB.open(OUTBOX_DB, 2);
    o.onupgradeneeded = () => {
      for (const name of ["events", "morning"] as const) if (!o.result.objectStoreNames.contains(name)) o.result.createObjectStore(name, { keyPath: "event.id" });
    };
    o.onsuccess = () => res(o.result);
    o.onerror = () => rej(o.error);
    o.onblocked = () => rej(new Error("blocked"));
  });
  const tx = (mode: IDBTransactionMode) => db.transaction(channel, mode).objectStore(channel);
  return {
    async all() {
      return (await req(tx("readonly").getAll())) as Stored<E>[];
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
function lazyStore<E extends { id: string }>(channel: Channel): Store<E> {
  let p: Promise<Store<E>> | null = null;
  const get = () =>
    (p ??= (async () => {
      try {
        return await idbStore<E>(channel);
      } catch {
        return memoryStore<E>();
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

export interface OutboxDeps<E extends { id: string } = ListenEvent> {
  store: Store<E>;
  post(events: E[]): Promise<PostResult>;
  /** navigator.sendBeacon; may throw or return false. `events` is the batch `json` carries. */
  beacon(json: string, events: E[]): boolean;
  /** fetch(..., {keepalive: true}) fallback; fire and forget. */
  keepalive(json: string, events: E[]): void;
  now(): number;
  /** Pending events that trigger a flush (default 10). */
  flushAt?: number;
  /** Splits events into the batches one beacon carries (default: slices of 100). */
  batches?(events: E[]): E[][];
  /** The beacon body of one batch (default `{events}`). */
  body?(events: E[]): string;
}

export function createOutbox<E extends { id: string } = ListenEvent>(deps: OutboxDeps<E>) {
  const flushAt = deps.flushAt ?? FLUSH_AT;
  let blockedUntil = 0;
  let flushing: Promise<void> | null = null;
  // Serialises store mutations so concurrent appends cannot interleave with the cap trim.
  let chain: Promise<unknown> = Promise.resolve();
  const serial = <T>(fn: () => Promise<T>): Promise<T> => {
    const r = chain.then(fn, fn);
    chain = r.catch(() => {});
    return r;
  };

  // Events appended in this page life that no flush has settled and no beacon has carried. A pagehide
  // handler cannot wait for storage, so onHide beacons these synchronously.
  const fresh = new Map<string, E>();

  const split = (events: E[]): E[][] => {
    if (deps.batches) return deps.batches(events);
    const out: E[][] = [];
    for (let i = 0; i < events.length; i += BATCH) out.push(events.slice(i, i + BATCH));
    return out;
  };

  const sendBeacon = (events: E[]) => {
    for (const batch of split(events)) {
      const json = deps.body ? deps.body(batch) : JSON.stringify({ events: batch });
      let ok: boolean;
      try {
        ok = deps.beacon(json, batch);
      } catch {
        ok = false;
      }
      if (!ok) {
        try {
          deps.keepalive(json, batch);
        } catch {
          /* nothing more to try; the next load flushes */
        }
      }
    }
  };

  const box = {
    dropped: 0,

    async pending(): Promise<number> {
      return (await deps.store.all()).length;
    },

    async snapshot(): Promise<E[]> {
      return (await deps.store.all()).map((r) => r.event);
    },

    appendMany(events: E[]): Promise<void> {
      for (const e of events) fresh.set(e.id, e);
      return serial(async () => {
        await deps.store.put(events.map((event) => ({ event, beaconed: false })));
        const all = await deps.store.all();
        if (all.length > CAP) {
          const drop = all.slice(0, all.length - CAP).map((r) => r.event.id);
          await deps.store.remove(drop);
          box.dropped += drop.length;
        }
        if (all.length >= flushAt) void box.flush();
      });
    },

    append(e: E): Promise<void> {
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
            for (const x of rows) fresh.delete(x.event.id);
            if (rows.length < BATCH) return;
          }
        } finally {
          flushing = null;
        }
      })();
      return flushing;
    },

    /**
     * pagehide / visibilitychange hidden: beacon what is not yet beaconed and mark it. The events of this
     * page life go out synchronously (nothing after pagehide is guaranteed to run); rows left by earlier
     * page lives follow from storage on a best-effort basis.
     */
    onHide(): Promise<void> {
      const mem = [...fresh.values()];
      fresh.clear();
      if (mem.length > 0) sendBeacon(mem);
      const sent = new Set(mem.map((e) => e.id));
      return serial(async () => {
        const rows = await deps.store.all();
        const older = rows.filter((r) => !r.beaconed && !sent.has(r.event.id));
        if (older.length > 0) sendBeacon(older.map((r) => r.event));
        const mark = rows.filter((r) => !r.beaconed && (sent.has(r.event.id) || older.includes(r)));
        if (mark.length > 0) await deps.store.put(mark.map((r) => ({ ...r, beaconed: true })));
      });
    },
  };
  return box;
}

export type Outbox<E extends { id: string } = ListenEvent> = ReturnType<typeof createOutbox<E>>;

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
  store: lazyStore<ListenEvent>("events"),
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

/** Batches of one crate each (the endpoint is per crate), at most 100 events a batch. */
export function morningBatches(events: MorningOutEvent[]): MorningOutEvent[][] {
  const by = new Map<string, MorningOutEvent[]>();
  for (const e of events) by.set(e.crate_id, [...(by.get(e.crate_id) ?? []), e]);
  const out: MorningOutEvent[][] = [];
  for (const list of by.values()) for (let i = 0; i < list.length; i += BATCH) out.push(list.slice(i, i + BATCH));
  return out;
}

const wire = (events: MorningOutEvent[]) => events.map(({ crate_id: _c, ...e }) => (void _c, e));
export const morningBeaconUrl = (crateId: string) => `/api/morning-crates/${encodeURIComponent(crateId)}/events?mm_client=spa`;

type MorningResult = { crate_id: string; revealed: boolean };
const morningListeners = new Set<(r: MorningResult) => void>();
/** Called after the server accepted a batch of one crate: `revealed` is true once the crate is revealed (maybe by that batch). */
export function onMorningResult(fn: (r: MorningResult) => void): () => void {
  morningListeners.add(fn);
  return () => void morningListeners.delete(fn);
}

/** Posts the queued events crate by crate. A batch that settled is safe to send again: the server counts a replayed id as a duplicate. */
export async function postMorning(events: MorningOutEvent[], client: typeof api = api, single = false): Promise<PostResult> {
  let worst: PostResult = { kind: "ok" };
  const batches = single ? events.map((e) => [e]) : morningBatches(events);
  for (const batch of batches) {
    const id = batch[0]!.crate_id;
    let r: PostResult;
    try {
      const res = await client.POST("/api/morning-crates/{id}/events", { params: { path: { id } }, body: { events: wire(batch) } });
      if (res.data) {
        for (const fn of morningListeners) fn({ crate_id: id, revealed: res.data.revealed });
        r = { kind: "ok" };
      } else {
        const s = res.response.status;
        const code = (res.error as { code?: string } | undefined)?.code;
        // never acceptable: a bad batch, a crate that is gone or not ready, a principal that may not submit.
        // A 403 that is not the server's own refusal (an expired proxy session or edge secret) is transient.
        const never = [400, 404, 409, 413, 422].includes(s) || (s === 403 && (code === "HUMAN_ONLY" || code === "FORBIDDEN_ROLE"));
        if (never && batch.length > 1) {
          // One bad event must not discard the valid ones: send them one at a time, the server refuses only its own.
          r = await postMorning(batch, client, true);
        } else r = never ? { kind: "drop" } : { kind: "retry" };
      }
    } catch (e) {
      r = e instanceof RateLimited ? { kind: "rate", retryAfterS: e.retryAfterS } : { kind: "retry" };
    }
    const rank = { ok: 0, drop: 1, retry: 2, rate: 3 } as const;
    if (rank[r.kind] > rank[worst.kind]) worst = r;
  }
  return worst;
}

/** The second channel: blind ratings, markers, skips and plays of the Morning Crate (C384.4). Flushed at once, beaconed on hide. */
export const morningOutbox: Outbox<MorningOutEvent> = createOutbox<MorningOutEvent>({
  store: lazyStore<MorningOutEvent>("morning"),
  post: postMorning,
  flushAt: 1,
  batches: morningBatches,
  body: (batch) => JSON.stringify({ events: wire(batch) }),
  beacon: (json, batch) => navigator.sendBeacon(morningBeaconUrl(batch[0]!.crate_id), new Blob([json], { type: "application/json" })),
  keepalive: (json, batch) => {
    void fetch(morningBeaconUrl(batch[0]!.crate_id), {
      method: "POST",
      body: json,
      keepalive: true,
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-MM-Client": "spa", "X-Requested-With": "XMLHttpRequest" },
    }).catch(() => {});
  },
  now: () => Date.now(),
});

interface Flushable {
  flush(): Promise<void>;
  onHide(): Promise<void>;
}

const beforeHide = new Set<() => void>();
/** Registers work that must queue its events before the outbox beacons on pagehide / hidden (a buffered marker). */
export function onBeforeHide(fn: () => void): () => void {
  beforeHide.add(fn);
  return () => void beforeHide.delete(fn);
}

/** Flush on load, every 30 s, on `online`; beacon on pagehide / hidden. Returns the uninstaller. */
export function startOutbox(box: Outbox = outbox, extra: Flushable[] = [morningOutbox]): () => void {
  const all: Flushable[] = [box, ...extra];
  const flush = () => all.forEach((b) => void b.flush());
  const hide = () => {
    for (const fn of beforeHide) {
      try {
        fn();
      } catch {
        /* a failing hook must not stop the beacon */
      }
    }
    all.forEach((b) => void b.onHide());
  };
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
