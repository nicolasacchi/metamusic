// Cache Storage and IndexedDB live under src/api/ (the lint rule that keeps storage and network access here).
// Used by the per-user browser state reset (lib/me.ts, C313.7). Names match api/sw/store.ts.
import { MEDIA_CACHE, OFFLINE_DB, OUTBOX_DB } from "../lib/me";

/** Delete the offline media chunks. */
export async function dropMediaCache(): Promise<void> {
  if (typeof caches !== "undefined") await caches.delete(MEDIA_CACHE);
}

function dropDb(name: string): Promise<void> {
  if (typeof indexedDB === "undefined") return Promise.resolve();
  return new Promise((resolve) => {
    const r = indexedDB.deleteDatabase(name);
    r.onsuccess = r.onerror = r.onblocked = () => resolve();
  });
}

/** Delete the offline metadata database (the service worker closes its connection on versionchange, see sw/store.ts). */
export function dropOfflineDb(): Promise<void> {
  return dropDb(OFFLINE_DB);
}

/** Delete the unflushed listen-event outbox, so one user's queued events never flush under the next user's session. */
export function dropOutboxDb(): Promise<void> {
  return dropDb(OUTBOX_DB);
}
