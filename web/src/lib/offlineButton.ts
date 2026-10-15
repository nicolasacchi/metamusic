// Offline-set edits for one track (optimistic locking via update_playlist, then the local copy).
import type { createApi } from "../api/client";
import type { components } from "../api/schema";

type Api = ReturnType<typeof createApi>;
type OfflineSet = components["schemas"]["OfflineSet"];
export type EditResult = { ok: true } | { ok: false; message: string };

export interface OfflineActions {
  download(trackId: string): Promise<void>;
  remove(trackId: string): Promise<void>;
}

async function readSet(api: Api): Promise<OfflineSet | string> {
  const r = await api.GET("/api/offline-set");
  return r.data ?? (r.error?.message || "Could not load the offline set");
}

/** Applies `edit` to the current set; one retry with a fresh read on a stale revision. */
async function update(api: Api, edit: (s: OfflineSet) => { add_track_ids: string[] } | { track_ids: string[] } | null): Promise<EditResult> {
  for (let attempt = 0; attempt < 2; attempt++) {
    const s = await readSet(api);
    if (typeof s === "string") return { ok: false, message: s };
    const change = edit(s);
    if (!change) return { ok: true };
    const r = await api.PATCH("/api/playlists/{id}", { params: { path: { id: s.playlist_id } }, body: { revision: s.revision, ...change } });
    if (r.data) return { ok: true };
    if (r.response.status === 409 && r.error?.code === "REVISION_CONFLICT" && attempt === 0) continue;
    return { ok: false, message: r.error?.message || "Could not update the offline set" };
  }
  return { ok: false, message: "The offline set changed; try again" };
}

export async function addToOfflineSet(api: Api, trackId: string, actions: OfflineActions): Promise<EditResult> {
  const r = await update(api, (s) => {
    if (s.items.some((i) => i.track_id === trackId)) return null;
    if (s.items.length >= s.limit_items) throw new FullError(s.limit_items);
    return { add_track_ids: [trackId] };
  }).catch((e: unknown) => (e instanceof FullError ? ({ ok: false, message: e.message } as const) : Promise.reject(e)));
  if (!r.ok) return r;
  await actions.download(trackId);
  return { ok: true };
}

export async function removeFromOfflineSet(api: Api, trackId: string, actions: OfflineActions): Promise<EditResult> {
  const r = await update(api, (s) => (s.items.some((i) => i.track_id === trackId) ? { track_ids: s.items.filter((i) => i.track_id !== trackId).map((i) => i.track_id) } : null));
  if (!r.ok) return r;
  await actions.remove(trackId);
  return { ok: true };
}

class FullError extends Error {
  constructor(limit: number) {
    super(`The offline set is full (${limit} tracks)`);
  }
}
