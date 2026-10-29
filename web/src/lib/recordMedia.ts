// Record and tape playback data without the player (L2-W3, C131.5). The player imports this module (boot restore
// of the gain map, tape links); recordPlayer.ts imports both, so nothing here may import the player.
import { api } from "../api/client";
import type { components } from "../api/schema";
import type { MediaLinks } from "./player.svelte";

export type RecordView = components["schemas"]["RecordView"];
export type Tape = components["schemas"]["Tape"];
export type TapeChapter = components["schemas"]["TapeChapter"];

/** Queue items of a tape: `tape:<id>`. Never persisted (isTrackId in playerStore.ts rejects the colon). */
export const TAPE_PREFIX = "tape:";
const RECORD_HREF = /^\/records\/([A-Za-z0-9_-]{1,64})$/;

export const isTapeItem = (id: string): boolean => id.startsWith(TAPE_PREFIX);
export const tapeItem = (tapeId: string): string => TAPE_PREFIX + tapeId;
export const tapeIdOf = (item: string): string => (isTapeItem(item) ? item.slice(TAPE_PREFIX.length) : item);
export const recordHref = (recordId: string): string => "/records/" + recordId;

/** The Record id of a queue source href (`/records/:id`), or null for any other source. */
export function recordIdOfHref(href: string | null | undefined): string | null {
  return RECORD_HREF.exec(href ?? "")?.[1] ?? null;
}

interface PlayableSlot {
  take: { id: string; status: string } | null;
  state: string;
}

const playable = (s: PlayableSlot): s is PlayableSlot & { take: { id: string; status: string } } =>
  s.take !== null && s.state !== "empty" && s.take.status !== "absent";

/** Resolved Take ids in slot order; empty slots are skipped and a Take that sits in two slots is queued once. */
export function takeQueue(view: Pick<RecordView, "slots">): string[] {
  const out: string[] = [];
  for (const s of view.slots) if (playable(s) && !out.includes(s.take.id)) out.push(s.take.id);
  return out;
}

/** take_id -> plan gain in dB; slots without a measurement (gain_db null) are left out and play at unity. */
export function gainMap(view: Pick<RecordView, "slots">): Record<string, number> {
  const out: Record<string, number> = {};
  for (const s of view.slots) if (playable(s) && typeof s.gain_db === "number" && Number.isFinite(s.gain_db) && !(s.take.id in out)) out[s.take.id] = s.gain_db;
  return out;
}

/** dB -> linear amplitude. */
export const dbToGain = (db: number): number => Math.pow(10, db / 20);

/** A Record's gain map from the server (a restored queue whose source is `/records/:id`); null when it cannot be read. */
export async function fetchRecordGains(recordId: string): Promise<Record<string, number> | null> {
  try {
    const r = await api.GET("/api/records/{id}", { params: { path: { id: recordId }, query: { include: ["tapes"] } } });
    return r.data ? gainMap(r.data) : null;
  } catch {
    return null;
  }
}

/** The tape as the player's link shape: one lossless file, always played as FLAC whatever the device quality. */
export function tapeToLinks(item: string, t: Tape, nowMs: number): MediaLinks | null {
  if (t.state !== "ready" || !t.download) return null;
  const exp = Date.parse(t.download.expires_at);
  const flac = {
    url: t.download.url,
    bytes: t.download.bytes,
    content_type: "audio/flac",
    sample_rate: 0,
    bits_per_sample: 24,
    sha256: t.sha256 ?? "",
  };
  return {
    track_id: item,
    expires_at: t.download.expires_at,
    ttl_s: Number.isFinite(exp) ? Math.max(0, Math.round((exp - nowMs) / 1000)) : 0,
    cover: null,
    lossy: t.contains_lossy,
    flac,
    // Unreachable: the player never falls back to MP3 for a tape item. Present only because the link shape requires it.
    mp3: { url: t.download.url, bytes: t.download.bytes, content_type: "audio/flac", bitrate_kbps: 0, sha256: flac.sha256 },
  };
}

/** Display names of tapes the app started this page life (tape id -> Record title). */
const tapeTitles = new Map<string, string>();
export function rememberTapeTitle(tapeId: string, title: string) {
  tapeTitles.set(tapeId, title);
}

export async function fetchTape(tapeId: string): Promise<Tape | null> {
  try {
    const r = await api.GET("/api/tapes/{id}", { params: { path: { id: tapeId } } });
    return r.data ?? null;
  } catch {
    return null;
  }
}

/** `fetchLinks` for a `tape:<id>` queue item: get_tape.download.url (re-read on every call, so a link past its expiry is re-minted). */
export async function tapeMediaLinks(item: string): Promise<MediaLinks | null> {
  const t = await fetchTape(tapeIdOf(item));
  return t ? tapeToLinks(item, t, Date.now()) : null;
}

/** `fetchMeta` for a `tape:<id>` queue item. */
export async function tapeMeta(item: string): Promise<{ title: string; album: string } | null> {
  const id = tapeIdOf(item);
  let title = tapeTitles.get(id);
  if (!title) {
    const t = await fetchTape(id);
    if (!t) return null;
    try {
      const r = await api.GET("/api/records/{id}", { params: { path: { id: t.record_id }, query: { include: ["tapes"] } } });
      title = r.data?.record.title;
    } catch {
      /* the generic label below */
    }
    if (title) tapeTitles.set(id, title);
  }
  return { title: title ?? "Tape", album: "Tape" };
}
