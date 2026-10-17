// Per-track judging state (rating, status, tags) with optimistic updates and rollback (C40.1).
import { api as appApi, RateLimited, type createApi } from "../api/client";
import type { components } from "../api/schema";
import { pushToast } from "./toast.svelte";

type Api = ReturnType<typeof createApi>;
type TagRef = components["schemas"]["TagRef"];
export type TrackStatus = NonNullable<components["schemas"]["UpdateTrackInBody"]["status"]>;

export interface TrackJudgment {
  /** The operator's stars (agent ratings are the read-only critic). */
  stars: number | null;
  verdict: string | null;
  critic: { stars: number | null; verdict: string | null } | null;
  status: string;
  tags: TagRef[];
}

/** Keyboard on the star control: 1-5 set, 0/Delete/Backspace clear (null), anything else undefined. */
export function starsFromKey(key: string): number | null | undefined {
  if (key >= "1" && key <= "5") return Number(key);
  if (key === "0" || key === "Delete" || key === "Backspace") return null;
  return undefined;
}

export function normalizeTag(s: string): string {
  return s.trim().toLowerCase();
}

export interface JudgeDeps {
  api: Api;
  /** Error toast sink. */
  notify(message: string): void;
}

function failure(e: unknown, fallback: string): string {
  if (e instanceof RateLimited) return `Too many changes — try again in ${e.retryAfterS} s`;
  return fallback;
}

export function createJudge(deps: JudgeDeps) {
  const entries = $state<Record<string, TrackJudgment>>({});

  /** Runs one mutation: `apply` is the optimistic change, `undo` restores the prior value on any failure. */
  async function mutate(
    id: string,
    apply: (e: TrackJudgment) => void,
    undo: (e: TrackJudgment) => void,
    send: () => Promise<{ ok: boolean; message?: string; commit?: (e: TrackJudgment) => void }>,
  ): Promise<boolean> {
    const e = entries[id];
    if (!e) return false;
    apply(e);
    try {
      const r = await send();
      if (r.ok) {
        r.commit?.(entries[id]!);
        return true;
      }
      undo(entries[id]!);
      deps.notify(r.message || "Could not save the change");
    } catch (err) {
      undo(entries[id]!);
      deps.notify(failure(err, "Could not save the change"));
    }
    return false;
  }

  return {
    seed(id: string, j: TrackJudgment) {
      entries[id] = { ...j, tags: j.tags.map((t) => ({ ...t, sources: [...t.sources] })) };
    },
    get(id: string): TrackJudgment | undefined {
      return entries[id];
    },

    rate(id: string, stars: number | null): Promise<boolean> {
      const prev = entries[id]?.stars ?? null;
      return mutate(
        id,
        (e) => void (e.stars = stars),
        (e) => void (e.stars = prev),
        async () => {
          const r = await deps.api.PUT("/api/tracks/{id}/rating", { params: { path: { id } }, body: { stars } });
          if (!r.data) return { ok: false, message: r.error?.message ?? "" };
          const d = r.data;
          return {
            ok: true,
            commit: (e) => {
              e.stars = d.human?.stars ?? null;
              e.verdict = d.human?.verdict ?? null;
              e.critic = d.critic ? { stars: d.critic.stars, verdict: d.critic.verdict } : null;
            },
          };
        },
      );
    },

    setStatus(id: string, status: TrackStatus): Promise<boolean> {
      const prev = entries[id]?.status ?? "";
      return mutate(
        id,
        (e) => void (e.status = status),
        (e) => void (e.status = prev),
        async () => {
          const r = await deps.api.PATCH("/api/tracks/{id}", { params: { path: { id } }, body: { status } });
          return r.data ? { ok: true } : { ok: false, message: r.error?.message ?? "" };
        },
      );
    },

    async addTag(id: string, raw: string): Promise<boolean> {
      const name = normalizeTag(raw);
      const e = entries[id];
      if (!name || !e || e.tags.some((t) => t.name === name)) return false;
      const prev = e.tags;
      return mutate(
        id,
        (x) => void (x.tags = [...x.tags, { name, sources: ["human"] }]),
        (x) => void (x.tags = prev),
        () => sendTags(id, { add: [name] }),
      );
    },

    async removeTag(id: string, raw: string): Promise<boolean> {
      const name = normalizeTag(raw);
      const e = entries[id];
      if (!e || !e.tags.some((t) => t.name === name)) return false;
      const prev = e.tags;
      return mutate(
        id,
        (x) => void (x.tags = x.tags.filter((t) => t.name !== name)),
        (x) => void (x.tags = prev),
        () => sendTags(id, { remove: [name] }),
      );
    },
  };

  async function sendTags(id: string, body: components["schemas"]["SetTagsInBody"]) {
    const r = await deps.api.PATCH("/api/tracks/{id}/tags", { params: { path: { id } }, body });
    if (!r.data) return { ok: false, message: r.error?.message ?? "" };
    const tags = r.data.tags;
    return { ok: true, commit: (e: TrackJudgment) => void (e.tags = tags) };
  }
}

export type Judge = ReturnType<typeof createJudge>;

/** Tag-name suggestions from one `list_tags` call per page; a failed call may be retried. */
export function createTagSuggestions(api: Api) {
  let cached: Promise<string[]> | null = null;
  return {
    names(): Promise<string[]> {
      cached ??= api
        .GET("/api/tags")
        .then((r) => (r.data ? r.data.items.map((t) => t.name) : null))
        .catch(() => null)
        .then((n) => {
          if (n === null) cached = null;
          return n ?? [];
        });
      return cached;
    },
    /** Names matching `prefix` (prefix matches first, then substring), minus `exclude`. */
    filter(names: readonly string[], prefix: string, exclude: readonly string[] = []): string[] {
      const p = normalizeTag(prefix);
      const pool = names.filter((n) => !exclude.includes(n));
      if (!p) return pool.slice(0, 8);
      return [...pool.filter((n) => n.startsWith(p)), ...pool.filter((n) => !n.startsWith(p) && n.includes(p))].slice(0, 8);
    },
  };
}

export { dismissToast, pushToast, toasts, type Toast } from "./toast.svelte";

/** The app-wide judging store. */
export const judge = createJudge({ api: appApi, notify: pushToast });
