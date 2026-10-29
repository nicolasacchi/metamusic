// Thin typed wrappers over the Crate operations and the Record slot edit that "Add to Record" needs (C131.3, C131.6).
// W1's curationApi.ts is not imported (it lands in parallel); these are this lane's own. Network only through the client.
import type { createApi } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { components, operations } from "../api/schema";
import { filterToQuery, type CrateFilter, type Subject } from "./crateFilter";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];
export type CrateSummary = S["CrateSummary"];
export type CrateDetail = S["CrateDetail"];
export type CrateMember = S["CrateMember"];
export type ItemsResult = S["ItemsResult"];
export type RecordSummary = S["RecordSummary"];

export interface Failure {
  ok: false;
  status: number;
  code: string;
  message: string;
  /** 409 STALE_REVISION: the caller shows "Changed elsewhere" and reloads. */
  stale: boolean;
}
export type Res<T> = { ok: true; value: T } | Failure;

/** Maps an error body to a Failure; the message is the server's, with a stable-code fallback per C129. */
export function failure(status: number, e: unknown, fallback: string): Failure {
  const o = typeof e === "object" && e !== null ? (e as { code?: unknown; message?: unknown }) : {};
  const code = typeof o.code === "string" ? o.code : "";
  const message = (typeof o.message === "string" && o.message) || CODE_TEXT[code] || fallback;
  return { ok: false, status, code, message, stale: status === 409 && code === "STALE_REVISION" };
}
const CODE_TEXT: Record<string, string> = {
  STALE_REVISION: "Changed elsewhere. Reload to continue.",
  CRATE_FULL: "This Crate is full (5 000 members).",
  CRATE_IS_SMART: "A smart Crate lists whatever matches its filter; edit the filter instead.",
  SONG_RETIRED: "That Song was merged into another one.",
  SONG_ALREADY_IN_RECORD: "That Song is already in this Record.",
  SUBJECT_MISMATCH: "That Crate holds the other kind of item.",
};
async function fail(r: { response: Response; error?: unknown }, fallback: string): Promise<Failure> {
  return failure(r.response.status, r.error, fallback);
}
async function guard<T>(fallback: string, run: () => Promise<Res<T>>): Promise<Res<T>> {
  try {
    return await run();
  } catch {
    return { ok: false, status: 0, code: "NETWORK", message: fallback + ": network error", stale: false };
  }
}

// ---- Crates ----

export interface CrateListQuery {
  subject?: Subject;
  kind?: "manual" | "smart";
  q?: string;
  includeHidden?: boolean;
  counts?: boolean;
  cursor?: string;
  limit?: number;
}
export function listCrates(api: Api, o: CrateListQuery = {}): Promise<Res<{ items: CrateSummary[]; next: string | null; total: number }>> {
  return guard("Could not load the Crates", async () => {
    const query: NonNullable<operations["list_crates"]["parameters"]["query"]> = {
      ...(o.subject ? { subject: o.subject } : {}),
      ...(o.kind ? { kind: o.kind } : {}),
      ...(o.q ? { q: o.q } : {}),
      ...(o.includeHidden ? { include_hidden: true } : {}),
      ...(o.counts ? { counts: true } : {}),
      ...(o.cursor ? { cursor: o.cursor } : {}),
      ...(o.limit ? { limit: o.limit } : {}),
    };
    const r = await api.GET("/api/crates", { params: { query } });
    return r.data ? { ok: true, value: { items: r.data.items, next: r.data.next_cursor, total: r.data.total } } : await fail(r, "Could not load the Crates");
  });
}

export function getCrate(api: Api, id: string): Promise<Res<CrateDetail>> {
  return guard("Could not load the Crate", async () => {
    const r = await api.GET("/api/crates/{id}", { params: { path: { id } } });
    if (r.data) return { ok: true, value: r.data };
    return r.response.status === 404 ? { ok: false, status: 404, code: "NOT_FOUND", message: "Crate not found", stale: false } : await fail(r, "Could not load the Crate");
  });
}

export interface NewCrateInput {
  name: string;
  description?: string;
  kind: "manual" | "smart";
  subject: Subject;
  filter?: CrateFilter;
  items?: CrateMember[];
}
/** create_crate. The returned function is one user action: a retry reuses the Idempotency-Key. */
export function createCrate(api: Api, input: NewCrateInput): () => Promise<Res<CrateDetail>> {
  const send = idempotentAction((key) =>
    api.POST("/api/crates", {
      params: { header: { "Idempotency-Key": key } },
      body: {
        name: input.name,
        kind: input.kind,
        subject: input.subject,
        ...(input.description ? { description: input.description } : {}),
        ...(input.kind === "smart" ? { filter: input.filter ?? {} } : {}),
        ...(input.kind === "manual" && input.items?.length ? { items: input.items } : {}),
      },
    }),
  );
  return () => guard("Could not create the Crate", async () => {
    const r = await send();
    return r.data ? { ok: true, value: r.data } : await fail(r, "Could not create the Crate");
  });
}

export interface CratePatch {
  name?: string;
  description?: string;
  filter?: CrateFilter;
  hidden?: boolean;
}
export function updateCrate(api: Api, id: string, revision: number, patch: CratePatch): Promise<Res<CrateDetail>> {
  return guard("Could not save the Crate", async () => {
    const r = await api.PATCH("/api/crates/{id}", { params: { path: { id } }, body: { revision, ...patch } });
    return r.data ? { ok: true, value: r.data } : await fail(r, "Could not save the Crate");
  });
}

export function editItems(api: Api, id: string, edit: { add?: CrateMember[]; remove?: CrateMember[] }): Promise<Res<ItemsResult>> {
  return guard("Could not change the Crate", async () => {
    const r = await api.POST("/api/crates/{id}/items", { params: { path: { id } }, body: edit });
    return r.data ? { ok: true, value: r.data } : await fail(r, "Could not change the Crate");
  });
}

/** Adds ids to a manual Crate, in calls of at most 200 (C125.1); the totals are summed. */
export async function addMembers(api: Api, crateId: string, subject: Subject, ids: readonly string[]): Promise<Res<ItemsResult>> {
  const total: ItemsResult = { added: 0, already: 0, removed: 0, not_member: 0, count: 0 };
  const uniq = [...new Set(ids)];
  for (let i = 0; i < uniq.length; i += 200) {
    const add = uniq.slice(i, i + 200).map((id): CrateMember => (subject === "song" ? { song_id: id } : { track_id: id }));
    const r = await editItems(api, crateId, { add });
    if (!r.ok) return r;
    total.added += r.value.added;
    total.already += r.value.already;
    total.count = r.value.count;
  }
  return { ok: true, value: total };
}

export function itemsText(r: ItemsResult, what: string): string {
  const parts = [`Added ${r.added}`];
  if (r.already > 0) parts.push(`${r.already} already in it`);
  return `${parts.join(", ")} to ${what}.`;
}

// ---- members and live counts (the list ops answer; `total` is the count) ----

export interface MemberRow {
  /** Song id (subject song) or Take id. */
  id: string;
  title: string;
  /** Song state or Take status; duration only for Takes. */
  detail: string;
  songId: string | null;
  durationS: number | null;
}
export interface MemberPage {
  items: MemberRow[];
  next: string | null;
  total: number;
}

type Query = Record<string, string | number | boolean | string[]>;

/** One page of a Crate's members, or of an unsaved filter (`query` = `{crate_id}` or a filter's list query). */
export function listMembers(api: Api, subject: Subject, query: Query): Promise<Res<MemberPage>> {
  return guard<MemberPage>("Could not load the list", async () => {
    if (subject === "song") {
      const r = await api.GET("/api/songs", { params: { query: query as NonNullable<operations["list_songs"]["parameters"]["query"]> } });
      if (!r.data) return await fail(r, "Could not load the list");
      return { ok: true, value: { total: r.data.total, next: r.data.next_cursor, items: r.data.items.map((s) => ({ id: s.id, title: s.title, detail: s.state, songId: s.id, durationS: null })) } };
    }
    const r = await api.GET("/api/tracks", { params: { query: query as NonNullable<operations["search_tracks"]["parameters"]["query"]> } });
    if (!r.data) return await fail(r, "Could not load the list");
    return { ok: true, value: { total: r.data.total, next: r.data.next_cursor, items: r.data.items.map((t) => ({ id: t.id, title: t.title, detail: t.status, songId: t.song_id, durationS: t.duration_s })) } };
  });
}

/** The live count of an unsaved filter: the same list op with limit 1, so the same Go code answers. */
export async function countMatches(api: Api, subject: Subject, filter: CrateFilter): Promise<Res<number>> {
  const r = await listMembers(api, subject, filterToQuery(filter, { limit: 1 }));
  return r.ok ? { ok: true, value: r.value.total } : r;
}

// ---- Records (Add to Record) ----

export function listRecordsForPicker(api: Api): Promise<Res<RecordSummary[]>> {
  return guard("Could not load the Records", async () => {
    const r = await api.GET("/api/records", { params: { query: { limit: 100 } } });
    return r.data ? { ok: true, value: r.data.items } : await fail(r, "Could not load the Records");
  });
}

export const MAX_RECORD_SONGS = 60;
const MAX_SLOT_OPS = 50;

/** The Song ids a "New Record from selection" can take: at most 60 (C129), with a note about the rest. */
export function capForRecord(ids: readonly string[]): { ids: string[]; note: string } {
  const uniq = [...new Set(ids)];
  if (uniq.length <= MAX_RECORD_SONGS) return { ids: uniq, note: "" };
  return { ids: uniq.slice(0, MAX_RECORD_SONGS), note: `A Record starts with at most ${MAX_RECORD_SONGS} Songs: the first ${MAX_RECORD_SONGS} are used, ${uniq.length - MAX_RECORD_SONGS} left out.` };
}

export function createRecord(api: Api, title: string, songIds: readonly string[]): () => Promise<Res<S["RecordView"]>> {
  const send = idempotentAction((key) =>
    api.POST("/api/records", { params: { header: { "Idempotency-Key": key } }, body: { title, ...(songIds.length ? { song_ids: [...songIds] } : {}) } }),
  );
  return () => guard("Could not create the Record", async () => {
    const r = await send();
    return r.data ? { ok: true, value: r.data } : await fail(r, "Could not create the Record");
  });
}

/** `edit_record_slots add` for each Song (50 ops per call); `pinTake` pins the one new slot of a single Song. A stale revision is re-read once. */
export async function addSongsToRecord(api: Api, record: { id: string; revision: number }, songIds: readonly string[], pinTake?: string): Promise<Res<{ added: number; revision: number }>> {
  const uniq = [...new Set(songIds)];
  let revision = record.revision;
  let added = 0;
  for (let i = 0; i < uniq.length; i += MAX_SLOT_OPS) {
    const ops = uniq.slice(i, i + MAX_SLOT_OPS).map((song_id) => ({ op: "add" as const, song_id, ...(pinTake && uniq.length === 1 ? { take_id: pinTake } : {}) }));
    const send = (rev: number) => guard<S["RecordView"]>("Could not add to the Record", async () => {
      const r = await api.POST("/api/records/{id}/slots", { params: { path: { id: record.id } }, body: { revision: rev, ops } });
      return r.data ? { ok: true, value: r.data } : await fail(r, "Could not add to the Record");
    });
    let r = await send(revision);
    if (!r.ok && r.stale) {
      const cur = await guard<number>("Could not add to the Record", async () => {
        const g = await api.GET("/api/records/{id}", { params: { path: { id: record.id }, query: { changes_limit: 0 } } });
        return g.data ? { ok: true, value: g.data.record.revision } : await fail(g, "Could not add to the Record");
      });
      if (cur.ok) r = await send(cur.value);
    }
    if (!r.ok) return r;
    revision = r.value.record.revision;
    added += ops.length;
  }
  return { ok: true, value: { added, revision } };
}
