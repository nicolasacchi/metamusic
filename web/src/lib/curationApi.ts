// Typed wrappers over the Records operations (C131.6). Failures are values, never throws: 409 STALE_REVISION
// becomes a reload action, 501 (an op another lane has not built yet) becomes "unavailable".
import type { createApi } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { components } from "../api/schema";
import type { SlotOp } from "./recordView";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];

export type RecordView = S["RecordView"];
export type Tape = S["Tape"];

export const reloadAction = "reload" as const;

export interface Failed {
  kind: "failed";
  status: number;
  code: string;
  message: string;
  details: Record<string, unknown>;
}
export type Result<T> =
  | { kind: "ok"; value: T }
  | { kind: "stale"; action: typeof reloadAction; message: string }
  | { kind: "unavailable" }
  | Failed
  | { kind: "network" };

interface Raw<T> {
  data?: T;
  error?: { code?: string; message?: string; details?: unknown };
  response: { status: number };
}

function settle<T>(r: Raw<T>): Result<T> {
  if (r.data !== undefined && r.response.status < 400) return { kind: "ok", value: r.data };
  const status = r.response.status;
  const code = r.error?.code ?? "";
  if (status === 501 || code === "NOT_IMPLEMENTED") return { kind: "unavailable" };
  if (status === 409 && code === "STALE_REVISION") return { kind: "stale", action: reloadAction, message: r.error?.message ?? "Changed elsewhere" };
  return { kind: "failed", status, code, message: r.error?.message ?? "", details: (r.error?.details ?? {}) as Record<string, unknown> };
}

async function call<T>(f: () => Promise<Raw<T>>): Promise<Result<T>> {
  try {
    return settle(await f());
  } catch {
    return { kind: "network" };
  }
}

export type Include = ("liner" | "energy" | "tapes" | "changes" | "preview")[];

export function listRecords(api: Api, query: { status?: ("draft" | "proposed" | "published")[]; q?: string; include_hidden?: boolean; limit?: number; cursor?: string }) {
  return call<S["RecordsOutBody"]>(() => api.GET("/api/records", { params: { query } }));
}

export function getRecord(api: Api, id: string, include: Include = ["liner", "energy", "changes", "tapes"], changesLimit?: number) {
  return call<RecordView>(() => api.GET("/api/records/{id}", { params: { path: { id }, query: { include, ...(changesLimit !== undefined ? { changes_limit: changesLimit } : {}) } } }));
}

/** One click = one action: the returned function may be retried (same key); a new click builds a new action. */
export function createRecord(api: Api, body: S["CreateRecordInBody"]): () => Promise<Result<RecordView>> {
  return idempotentAction((key) => call<RecordView>(() => api.POST("/api/records", { params: { header: { "Idempotency-Key": key } }, body })));
}

export function updateRecord(api: Api, id: string, body: S["UpdateRecordInBody"]) {
  return call<RecordView>(() => api.PATCH("/api/records/{id}", { params: { path: { id } }, body }));
}

export function editSlots(api: Api, id: string, revision: number, ops: SlotOp[]) {
  return call<RecordView>(() => api.POST("/api/records/{id}/slots", { params: { path: { id } }, body: { revision, ops } }));
}

/** The Freeze shortcut: exactly one pin_all op. */
export function freezeAll(api: Api, id: string, revision: number) {
  return editSlots(api, id, revision, [{ op: "pin_all" }]);
}

export function getChanges(api: Api, id: string, query: { order?: "asc" | "desc"; cursor?: number; limit?: number } = {}) {
  return call<S["RecordChangesOutBody"]>(() => api.GET("/api/records/{id}/changes", { params: { path: { id }, query } }));
}

export function proposeRecord(api: Api, id: string, revision: number, rationale: string) {
  return call<RecordView>(() => api.POST("/api/records/{id}/proposal", { params: { path: { id } }, body: { revision, rationale } }));
}

export function publishRecord(api: Api, id: string, revision: number, published: boolean) {
  return call<RecordView>(() => api.POST("/human/api/records/{id}/publish", { params: { path: { id } }, body: { revision, published } }));
}

export function exportTape(api: Api, id: string, body: S["ExportRecordTapeInBody"]): () => Promise<Result<Tape>> {
  return idempotentAction((key) => call<Tape>(() => api.POST("/api/records/{id}/tapes", { params: { path: { id }, header: { "Idempotency-Key": key } }, body })));
}

export function getTape(api: Api, id: string) {
  return call<Tape>(() => api.GET("/api/tapes/{id}", { params: { path: { id } } }));
}

/** Typeahead for the SongPicker. */
export async function searchSongs(api: Api, q: string, limit = 8): Promise<S["SongSummary"][]> {
  try {
    const r = await api.GET("/api/songs", { params: { query: { ...(q ? { q } : {}), limit } } });
    return r.data?.items ?? [];
  } catch {
    return [];
  }
}

/** The Song's Takes, for the pin menu. */
export async function songTakes(api: Api, songId: string): Promise<S["TakeCard"][]> {
  try {
    const r = await api.GET("/api/songs/{id}", { params: { path: { id: songId } } });
    return (r.data?.takes ?? []).filter((t) => t.status !== "discarded");
  } catch {
    return [];
  }
}
