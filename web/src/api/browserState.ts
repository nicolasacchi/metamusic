// Cache Storage and IndexedDB live under src/api/ (the lint rule that keeps storage and network access here).
// Used by the per-user browser state reset (lib/me.ts, C313.7). Names match api/sw/store.ts.
import { MEDIA_CACHE, OFFLINE_DB } from "../lib/me";

/** Delete the offline media chunks. */
export async function dropMediaCache(): Promise<void> {
  if (typeof caches !== "undefined") await caches.delete(MEDIA_CACHE);
}

/** Delete the offline metadata database; resolves when done, refused or blocked (a blocked delete finishes when the other tab closes). */
export function dropOfflineDb(): Promise<void> {
  if (typeof indexedDB === "undefined") return Promise.resolve();
  return new Promise((resolve) => {
    const r = indexedDB.deleteDatabase(OFFLINE_DB);
    r.onsuccess = r.onerror = r.onblocked = () => resolve();
  });
}
