// Typed wrappers over the Muse/Session operations the Board uses. Failures are values, never throws.
import type { createApi } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { components } from "../api/schema";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];
export type Muse = S["Muse"];

export interface Failed {
  kind: "failed";
  status: number;
  code: string;
  message: string;
  details: Record<string, unknown>;
}
export type Result<T> = { kind: "ok"; value: T } | Failed | { kind: "network" };

function failed(status: number, e: { code?: string; message?: string; details?: unknown } | undefined): Failed {
  return { kind: "failed", status, code: e?.code ?? "", message: e?.message ?? "", details: (e?.details ?? {}) as Record<string, unknown> };
}

export type CreateMuseOutcome = { kind: "created"; muse: Muse } | { kind: "existing"; muse: Muse; hidden: boolean } | Failed | { kind: "network" };

/** One click = one action: the returned function may be retried (same key); a new click builds a new action. */
export function createMuse(api: Api, body: S["CreateMuseInBody"]): () => Promise<CreateMuseOutcome> {
  return idempotentAction(async (key): Promise<CreateMuseOutcome> => {
    try {
      const r = await api.POST("/api/muses", { params: { header: { "Idempotency-Key": key } }, body });
      if (r.data) return { kind: "created", muse: r.data };
      const status = r.response.status;
      const details = (r.error?.details ?? {}) as Record<string, unknown>;
      if (status === 409 && r.error?.code === "MUSE_EXISTS" && typeof details.muse_id === "string") {
        const g = await api.GET("/api/muses/{id}", { params: { path: { id: details.muse_id } } });
        if (g.data) return { kind: "existing", muse: g.data.muse, hidden: details.hidden === true };
      }
      return failed(status, r.error);
    } catch {
      return { kind: "network" };
    }
  });
}

export async function assignSongs(api: Api, museId: string, songIds: string[]): Promise<Result<S["AssignResult"]>> {
  try {
    const r = await api.POST("/api/muses/{id}/songs", { params: { path: { id: museId } }, body: { song_ids: songIds } });
    return r.data ? { kind: "ok", value: r.data } : failed(r.response.status, r.error);
  } catch {
    return { kind: "network" };
  }
}

export async function searchMuses(api: Api, q: string, limit = 8): Promise<S["MuseSummary"][]> {
  try {
    const r = await api.GET("/api/muses", { params: { query: { ...(q ? { q } : {}), limit } } });
    return r.data?.items ?? [];
  } catch {
    return [];
  }
}

export async function updateSession(api: Api, id: string, body: S["UpdateSessionInBody"]): Promise<Result<S["SessionSummary"]>> {
  try {
    const r = await api.PATCH("/api/sessions/{id}", { params: { path: { id } }, body });
    return r.data ? { kind: "ok", value: r.data } : failed(r.response.status, r.error);
  } catch {
    return { kind: "network" };
  }
}
