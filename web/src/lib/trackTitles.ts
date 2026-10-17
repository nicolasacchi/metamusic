// Titles for queue entries: one GET /api/tracks/{id} per track, cached for the session. Failures are not cached.
import { api } from "../api/client";

const titles = new Map<string, string>();
const inflight = new Map<string, Promise<string | null>>();

export function cachedTitle(id: string): string | undefined {
  return titles.get(id);
}

export function loadTitle(id: string): Promise<string | null> {
  const hit = titles.get(id);
  if (hit !== undefined) return Promise.resolve(hit);
  let p = inflight.get(id);
  if (!p) {
    p = api
      .GET("/api/tracks/{id}", { params: { path: { id } } })
      .then((r) => {
        if (!r.data) return null;
        titles.set(id, r.data.title);
        return r.data.title;
      })
      .catch(() => null)
      .finally(() => inflight.delete(id));
    inflight.set(id, p);
  }
  return p;
}
