// Pure helpers for the M2a-W3 pages (playlists, export, backups, audit, context, unpair).
// Everything here is DOM-free so the components stay thin.
import type { createApi } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { components } from "../api/schema";

type Api = ReturnType<typeof createApi>;
type Export = components["schemas"]["Export"];
type ExportJSONLInfo = components["schemas"]["ExportJSONLInfo"];
type ExportValidation = components["schemas"]["ExportValidation"];
type ExportBody = components["schemas"]["ExportJSONLInBody"];

function message(e: unknown, fallback: string): string {
  if (typeof e === "object" && e !== null && "message" in e && typeof (e as { message: unknown }).message === "string") {
    return (e as { message: string }).message || fallback;
  }
  return fallback;
}
function code(e: unknown): string {
  return typeof e === "object" && e !== null && "code" in e ? String((e as { code: unknown }).code) : "";
}

// ---- export selection <-> request body ----------------------------------------------------------

export type SelectionKind = ExportBody["selection"];

export interface SelectionForm {
  kind: SelectionKind;
  sourcePath: string;
  forBatch: boolean;
  filename: string;
  trackStatus: string[];
  /** Text so a half-typed number survives editing. */
  minStars: string;
  /** Comma separated. */
  tags: string;
  playlistId: string;
  draftStatus: string[];
  collection: string;
  origin: string;
  groupKey: string;
}

export function emptySelection(): SelectionForm {
  return {
    kind: "source",
    sourcePath: "",
    forBatch: false,
    filename: "",
    trackStatus: [],
    minStars: "",
    tags: "",
    playlistId: "",
    draftStatus: [],
    collection: "",
    origin: "",
    groupKey: "",
  };
}

function splitList(s: string): string[] {
  return s
    .split(",")
    .map((x) => x.trim())
    .filter((x) => x !== "");
}

export function toBody(f: SelectionForm): ExportBody {
  const body: ExportBody = { selection: f.kind };
  if (f.kind === "source") {
    if (f.sourcePath.trim()) body.source_path = f.sourcePath.trim();
    if (f.forBatch) body.for_batch = true;
  } else if (f.kind === "tracks") {
    const t: NonNullable<ExportBody["tracks"]> = {};
    if (f.trackStatus.length > 0) t.status = f.trackStatus as NonNullable<typeof t.status>;
    const stars = Number(f.minStars);
    if (f.minStars.trim() !== "" && Number.isInteger(stars) && stars >= 1 && stars <= 5) t.min_stars = stars;
    const tags = splitList(f.tags);
    if (tags.length > 0) t.tags = tags;
    if (f.playlistId.trim()) t.playlist_id = f.playlistId.trim();
    if (Object.keys(t).length > 0) body.tracks = t;
  } else {
    const d: NonNullable<ExportBody["drafts"]> = {};
    if (f.draftStatus.length > 0) d.status = f.draftStatus as NonNullable<typeof d.status>;
    if (f.collection.trim()) d.collection = f.collection.trim();
    if (f.origin.trim()) d.origin = f.origin.trim();
    if (f.groupKey.trim()) d.group_key = f.groupKey.trim();
    if (Object.keys(d).length > 0) body.drafts = d;
  }
  if (f.filename.trim()) body.filename = f.filename.trim();
  return body;
}

export function fromBody(b: ExportBody): SelectionForm {
  const f = emptySelection();
  f.kind = b.selection;
  f.sourcePath = b.source_path ?? "";
  f.forBatch = b.for_batch ?? false;
  f.filename = b.filename ?? "";
  f.trackStatus = [...(b.tracks?.status ?? [])];
  f.minStars = b.tracks?.min_stars === undefined ? "" : String(b.tracks.min_stars);
  f.tags = (b.tracks?.tags ?? []).join(", ");
  f.playlistId = b.tracks?.playlist_id ?? "";
  f.draftStatus = [...(b.drafts?.status ?? [])];
  f.collection = b.drafts?.collection ?? "";
  f.origin = b.drafts?.origin ?? "";
  f.groupKey = b.drafts?.group_key ?? "";
  return f;
}

// ---- result summaries -------------------------------------------------------------------------

export function summaryLine(info: Pick<ExportJSONLInfo, "rows" | "raw_rows" | "generated_rows">): string {
  return `${info.rows} ${info.rows === 1 ? "row" : "rows"} · ${info.raw_rows} raw · ${info.generated_rows} regenerated`;
}

export interface ValidationView {
  tone: "ok" | "original" | "failed";
  label: string;
}

/**
 * A `source` export without for_batch is the original file byte for byte, so whatever the validator
 * finds is a property of that file, not a failure of the export.
 */
export function validationView(v: ExportValidation, sel: { kind: SelectionKind; forBatch: boolean }): ValidationView {
  if (v.ok) return { tone: "ok", label: "OK" };
  const n = v.issues.length;
  const count = `${n} ${n === 1 ? "issue" : "issues"}`;
  if (sel.kind === "source" && !sel.forBatch) return { tone: "original", label: `${count} — issues of the original file` };
  return { tone: "failed", label: `Validation failed: ${count}` };
}

// ---- opening / refreshing an export -------------------------------------------------------------

/** True when the signed link is expired or within one minute of expiring. */
export function needsRefresh(expiresAt: string, now: number): boolean {
  const t = Date.parse(expiresAt);
  return !Number.isFinite(t) || t - now <= 60_000;
}

export interface ExportRef {
  id: string;
  kind?: SelectionKind;
  forBatch?: boolean;
}

const KINDS: SelectionKind[] = ["source", "tracks", "drafts"];

export function exportIdFromSearch(search: string): ExportRef | null {
  const q = new URLSearchParams(search);
  const id = q.get("export");
  if (!id) return null;
  const ref: ExportRef = { id };
  const sel = q.get("sel") as SelectionKind | null;
  if (sel && KINDS.includes(sel)) ref.kind = sel;
  if (q.get("batch") === "1") ref.forBatch = true;
  return ref;
}

export function exportSearch(id: string, sel: { kind: SelectionKind; forBatch: boolean }): string {
  const q = new URLSearchParams({ export: id, sel: sel.kind });
  if (sel.forBatch) q.set("batch", "1");
  return "?" + q.toString();
}

export type FetchedExport = { ok: true; export: Export } | { ok: false; expired: boolean; message: string };

export async function fetchExport(api: Api, id: string): Promise<FetchedExport> {
  const r = await api.GET("/api/exports/{id}", { params: { path: { id } } });
  if (r.data) return { ok: true, export: r.data };
  if (r.response.status === 410 || code(r.error) === "EXPORT_EXPIRED") {
    return { ok: false, expired: true, message: "export expired — create it again" };
  }
  return { ok: false, expired: false, message: message(r.error, "Could not load the export") };
}

export async function createJsonlExport(api: Api, f: SelectionForm): Promise<FetchedExport> {
  const body = toBody(f);
  const send = idempotentAction(async (key) => api.POST("/api/exports/jsonl", { params: { header: { "Idempotency-Key": key } }, body }));
  const r = await send();
  if (r.data) return { ok: true, export: r.data };
  return { ok: false, expired: false, message: message(r.error, "Could not create the export") };
}

// ---- playlists ----------------------------------------------------------------------------------

/** Moves the item at `from` by `delta` places; out-of-range moves return an unchanged copy. */
export function moveItem<T>(items: readonly T[], from: number, delta: number): T[] {
  const out = [...items];
  const to = from + delta;
  if (from < 0 || from >= out.length || to < 0 || to >= out.length || delta === 0) return out;
  const [x] = out.splice(from, 1);
  out.splice(to, 0, x as T);
  return out;
}

export type PlaylistResult = { ok: true; playlist: components["schemas"]["Playlist"] } | { ok: false; message: string };

/** Appends one track; on a stale revision re-reads the playlist and retries once. */
export async function addToPlaylist(api: Api, pl: { id: string; revision: number }, trackId: string): Promise<PlaylistResult> {
  const patch = (revision: number) =>
    api.PATCH("/api/playlists/{id}", { params: { path: { id: pl.id } }, body: { revision, add_track_ids: [trackId] } });
  let r = await patch(pl.revision);
  if (!r.data && r.response.status === 409) {
    const cur = await api.GET("/api/playlists/{id}", { params: { path: { id: pl.id } } });
    if (cur.data) r = await patch(cur.data.revision);
  }
  return r.data ? { ok: true, playlist: r.data } : { ok: false, message: message(r.error, "Could not add to the playlist") };
}

export async function createPlaylist(api: Api, name: string, trackIds: string[] = []): Promise<PlaylistResult> {
  const send = idempotentAction(async (key) =>
    api.POST("/api/playlists", { params: { header: { "Idempotency-Key": key } }, body: { name, ...(trackIds.length > 0 ? { track_ids: trackIds } : {}) } }),
  );
  const r = await send();
  return r.data ? { ok: true, playlist: r.data } : { ok: false, message: message(r.error, "Could not create the playlist") };
}

/** Saves the full order; a stale revision is reported as such (the page reloads, nothing is merged). */
export async function savePlaylist(
  api: Api,
  pl: { id: string; revision: number },
  patch: { name?: string; description?: string; hidden?: boolean; trackIds?: string[] },
): Promise<PlaylistResult & { conflict?: boolean }> {
  const r = await api.PATCH("/api/playlists/{id}", {
    params: { path: { id: pl.id } },
    body: {
      revision: pl.revision,
      ...(patch.name !== undefined ? { name: patch.name } : {}),
      ...(patch.description !== undefined ? { description: patch.description } : {}),
      ...(patch.hidden !== undefined ? { hidden: patch.hidden } : {}),
      ...(patch.trackIds !== undefined ? { track_ids: patch.trackIds } : {}),
    },
  });
  if (r.data) return { ok: true, playlist: r.data };
  return { ok: false, message: message(r.error, "Could not save the playlist"), ...(r.response.status === 409 ? { conflict: true } : {}) };
}

export async function createPlaylistZip(api: Api, id: string): Promise<FetchedExport> {
  const send = idempotentAction(async (key) => api.POST("/api/playlists/{id}/exports", { params: { path: { id }, header: { "Idempotency-Key": key } } }));
  const r = await send();
  return r.data ? { ok: true, export: r.data } : { ok: false, expired: false, message: message(r.error, "Could not create the ZIP") };
}

// ---- audit --------------------------------------------------------------------------------------

type AuditQuery = NonNullable<NonNullable<import("../api/schema").operations["get_audit"]["parameters"]["query"]>>;
export type PrincipalKind = NonNullable<AuditQuery["principal_kind"]>[number];

export const LISTEN_OP = "log_listen_events";

export interface AuditFilters {
  principalKind: PrincipalKind[];
  /** Comma separated operation ids. */
  op: string;
  since: string;
  until: string;
  statusMin: string;
  /** Off by default: listen events are noise in the audit trail. */
  includeListen: boolean;
}

export function defaultAuditFilters(): AuditFilters {
  return { principalKind: [], op: "", since: "", until: "", statusMin: "", includeListen: false };
}

export function auditQuery(f: AuditFilters, cursor?: string): AuditQuery {
  const q: AuditQuery = { limit: 50 };
  if (f.principalKind.length > 0) q.principal_kind = [...f.principalKind];
  const ops = splitList(f.op);
  if (ops.length > 0) q.op = ops;
  if (!f.includeListen) q.exclude_op = [LISTEN_OP];
  if (f.since.trim()) q.since = f.since.trim();
  if (f.until.trim()) q.until = f.until.trim();
  // bind:value on a number input yields a number (null when cleared), not the declared string
  const raw = String(f.statusMin ?? "").trim();
  const s = Number(raw);
  if (raw !== "" && Number.isInteger(s) && s >= 100 && s <= 599) q.status_min = s;
  if (cursor) q.cursor = cursor;
  return q;
}

export function auditFiltersFromQuery(q: AuditQuery): AuditFilters {
  return {
    principalKind: [...(q.principal_kind ?? [])],
    op: (q.op ?? []).join(", "),
    since: q.since ?? "",
    until: q.until ?? "",
    statusMin: q.status_min === undefined ? "" : String(q.status_min),
    includeListen: !(q.exclude_op ?? []).includes(LISTEN_OP),
  };
}

// ---- agent context ------------------------------------------------------------------------------

export interface ClipboardLike {
  writeText(text: string): Promise<void>;
}

export type ContextResult = { ok: true; copied: boolean; chars: number } | { ok: false; message: string };

/**
 * Fetches the context bundle as the exact REST body text (so the count equals `_budget.chars`),
 * copies it and reports its UTF-16 length. A refused clipboard write still reports the length.
 */
export async function copyContext(api: Api, trackId: string, clipboard: ClipboardLike | undefined): Promise<ContextResult> {
  const r = await api.GET("/api/tracks/{id}/context", { params: { path: { id: trackId } }, parseAs: "text" });
  if (typeof r.data !== "string") return { ok: false, message: message(r.error, "Could not load the context") };
  const text = r.data.trimEnd();
  let copied = false;
  try {
    if (clipboard) {
      await clipboard.writeText(text);
      copied = true;
    }
  } catch {
    copied = false;
  }
  return { ok: true, copied, chars: text.length };
}

// ---- unpair -------------------------------------------------------------------------------------

/** revoke_own_device, then a full-page navigation to the chooser (the cookie is gone). */
export async function unpairBrowser(api: Api, assign: (url: string) => void): Promise<boolean> {
  const r = await api.POST("/api/devices/self/revoke");
  if (!r.data) return false;
  assign("/start");
  return true;
}
