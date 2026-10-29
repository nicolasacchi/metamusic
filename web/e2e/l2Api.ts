// API helpers for the L2 specs (Records, Crates, tapes, curation). Like the L1 specs, every spec makes its own Songs under a
// per-run unique title, so no spec depends on a total or a Pick another spec changes. Nothing here deletes: Records are
// hidden and Crates hidden when a spec is done with them.
import type { APIRequestContext, PlaywrightWorkerArgs } from "@playwright/test";
import { apiJSON, uniq } from "./helpers";
import { composeSong, getSong, waitTakes, type ComposedSong } from "./l1Api";

export interface SlotOut {
  id: string;
  position: number;
  mode: "floating" | "pinned";
  state: string;
  side: string | null;
  song: { id: string; title: string };
  take: { id: string; title: string } | null;
}
export interface RecordOut {
  record: { id: string; title: string; status: "draft" | "proposed" | "published"; revision: number; hidden: boolean };
  slots: SlotOut[];
  tapes?: TapeOut[] | null;
  warnings: { code: string; slot_id: string }[];
  totals: { slots: number; playable: number; undecided: number };
  pending: boolean;
}
export interface TapeOut {
  id: string;
  state: "queued" | "running" | "ready" | "failed" | "expired";
  record_revision: number;
  chapters: { index: number; slot_id: string; start_ms: number }[] | null;
  contains_lossy: boolean;
  download: { url: string } | null;
  warnings: { code: string }[];
}
export interface ChangeOut {
  id: number;
  kind: string;
}
export interface CrateOut {
  crate: { id: string; name: string; kind: "manual" | "smart"; revision: number; hidden: boolean };
  count?: number;
}

export interface TitledSong extends ComposedSong {
  title: string;
}

/** A rendered Song of `takes` takes under a fresh unique title (the Songs picker and the Board filter find it by that). */
export async function titledSong(request: APIRequestContext, tag: string, takes: number): Promise<TitledSong> {
  const s = await composeSong(request, tag, takes);
  const title = uniq(`${tag} song`);
  const rev = (await getSong(request, s.songId)).song.revision;
  await apiJSON(request, "PATCH", `/api/songs/${s.songId}`, { revision: rev, title });
  return { ...s, title };
}

/** The Take of the Song that is not its Pick. */
export async function otherTake(request: APIRequestContext, songId: string): Promise<string> {
  const d = await waitTakes(request, songId, 2);
  const t = d.takes.find((x) => !x.is_pick);
  if (!t) throw new Error("a Song with two takes has a take that is not the Pick");
  return t.id;
}

export const rate = (request: APIRequestContext, takeId: string, stars: number | null) => apiJSON(request, "PUT", `/api/tracks/${takeId}/rating`, { stars });

export const createRecord = (request: APIRequestContext, title: string, songIds: string[] = []) =>
  apiJSON<RecordOut>(request, "POST", "/api/records", { title, ...(songIds.length ? { song_ids: songIds } : {}) }, true);

export const getRecord = (request: APIRequestContext, id: string) => apiJSON<RecordOut>(request, "GET", `/api/records/${id}?include=tapes`);

export const editSlots = (request: APIRequestContext, id: string, revision: number, ops: unknown[]) => apiJSON<RecordOut>(request, "POST", `/api/records/${id}/slots`, { revision, ops });

export async function changes(request: APIRequestContext, id: string): Promise<ChangeOut[]> {
  return (await apiJSON<{ items: ChangeOut[] }>(request, "GET", `/api/records/${id}/changes?order=desc&limit=50`)).items;
}

/** Polls the Record's changelog until a change of `kind` exists, within `timeoutMs`. */
export async function waitChange(request: APIRequestContext, id: string, kind: string, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if ((await changes(request, id)).some((c) => c.kind === kind)) return true;
    if (Date.now() >= deadline) return false;
    await new Promise((r) => setTimeout(r, 150));
  }
}

/** A published Record of the given Songs: every slot pinned (Freeze), proposed, then published by the human principal. */
export async function publishedRecord(request: APIRequestContext, title: string, songIds: string[]): Promise<RecordOut> {
  let v = await createRecord(request, title, songIds);
  v = await editSlots(request, v.record.id, v.record.revision, [{ op: "pin_all" }]);
  v = await apiJSON<RecordOut>(request, "POST", `/api/records/${v.record.id}/proposal`, { revision: v.record.revision, rationale: "e2e: ready" });
  return apiJSON<RecordOut>(request, "POST", `/human/api/records/${v.record.id}/publish`, { revision: v.record.revision, published: true });
}

/** Hides a Record (the spec's reset): published Records are unpublished first. */
export async function retireRecord(request: APIRequestContext, id: string): Promise<void> {
  let v = await getRecord(request, id);
  if (v.record.status === "published") v = await apiJSON<RecordOut>(request, "POST", `/human/api/records/${id}/publish`, { revision: v.record.revision, published: false });
  await apiJSON(request, "PATCH", `/api/records/${id}`, { revision: v.record.revision, hidden: true });
}

export const createCrate = (request: APIRequestContext, body: Record<string, unknown>) => apiJSON<CrateOut>(request, "POST", "/api/crates", body, true);

export async function hideCrate(request: APIRequestContext, id: string): Promise<void> {
  const c = await apiJSON<CrateOut>(request, "GET", `/api/crates/${id}`);
  await apiJSON(request, "PATCH", `/api/crates/${id}`, { revision: c.crate.revision, hidden: true });
}

/** A request context that acts as the agent principal (bearer token, no proxy identity). */
export function agentContext(playwright: PlaywrightWorkerArgs["playwright"], baseURL: string | undefined, token: string) {
  return playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
}

export const curationSettings = (request: APIRequestContext) => apiJSON<{ export_muse_names: boolean; subsonic_muse_names: boolean }>(request, "GET", "/api/curation/settings");
