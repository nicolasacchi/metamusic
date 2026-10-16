// API helpers for the L1 specs (Board, Song, Sounds, Muse). Like the M3 specs, every L1 spec makes its own data
// (Muses, draft Songs, composed Songs with their takes) under a per-run unique tag, so no spec depends on a total
// or a Pick another spec changes. Songs are derived by the Reconciler a moment after their draft or track exists,
// hence the polling helpers. Nothing here deletes: drafts are archived when a spec is done with them.
import type { APIRequestContext } from "@playwright/test";
import { apiJSON, uniq } from "./helpers";
import { allTerminal, assertOnlyDuplicateFailures, createHuman, estimate, first, newSeedChildren, pollBatch, recipe } from "./m3Api";

export interface SongSummary {
  id: string;
  title: string;
  angle: string | null;
  muse_id: string | null;
  draft_id: string | null;
  root_track_id: string | null;
  state: "draft" | "rendered" | "judged" | "picked";
  takes: number;
  pick: { take_id: string | null; source: string } | null;
}
export interface SongList {
  items: SongSummary[];
  next_cursor: string | null;
  total: number;
}
export interface SongDetail {
  song: { id: string; revision: number; title: string };
  takes: { id: string; title: string; is_pick: boolean; pinned: boolean }[];
  takes_total: number;
  pending: boolean;
  pick: { take_id: string | null; source: string; pinned_take_id: string | null; pending: boolean };
  proposals: { id: string; take_id: string; state: string }[];
}
export interface MuseOut {
  id: string;
  title: string;
  artist: string | null;
  revision: number;
}
interface DraftOut {
  id: string;
  revision: number;
}

/** A lower-case tag of letters only (the Sounds taxonomy treats digits as noise inside a style term). */
export function letters(prefix: string): string {
  const hex = uniq("").slice(1); // random hex
  return prefix + [...hex].map((c) => String.fromCharCode("g".charCodeAt(0) + parseInt(c, 16))).join("");
}

export function createMuse(request: APIRequestContext, title: string, artist: string | null = null): Promise<MuseOut> {
  return apiJSON<MuseOut>(request, "POST", "/api/muses", { kind: "reference", title, ...(artist ? { artist } : {}) }, true);
}

export function getMuse(request: APIRequestContext, id: string): Promise<{ muse: MuseOut & { hidden: boolean } }> {
  return apiJSON(request, "GET", `/api/muses/${id}`);
}

export function getSong(request: APIRequestContext, id: string): Promise<SongDetail> {
  return apiJSON<SongDetail>(request, "GET", `/api/songs/${id}`);
}

export function assignSongs(request: APIRequestContext, museId: string, songIds: string[]): Promise<{ assigned: number; unchanged: number }> {
  return apiJSON(request, "POST", `/api/muses/${museId}/songs`, { song_ids: songIds });
}

/** Polls until the list for `query` holds at least `min` Songs (the Reconciler derives Songs asynchronously). */
export async function waitSongs(request: APIRequestContext, query: string, min = 1, timeoutMs = 60_000): Promise<SongSummary[]> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const items = (await apiJSON<SongList>(request, "GET", `/api/songs?${query}`)).items;
    if (items.length >= min) return items;
    await new Promise((r) => setTimeout(r, 300));
  }
  throw new Error(`no Song for ${query} within ${timeoutMs} ms`);
}

/** Polls until the Song has `n` takes and a settled Pick (no recompute pending; children join the Song of their root). */
export async function waitTakes(request: APIRequestContext, songId: string, n: number, timeoutMs = 60_000): Promise<SongDetail> {
  const deadline = Date.now() + timeoutMs;
  let last: SongDetail | undefined;
  while (Date.now() < deadline) {
    last = await getSong(request, songId);
    if (last.takes_total >= n && !last.pending && !last.pick.pending && last.pick.take_id !== null && last.takes.some((t) => t.is_pick)) return last;
    await new Promise((r) => setTimeout(r, 300));
  }
  throw new Error(`Song ${songId} did not reach ${n} takes with a Pick (has ${last?.takes_total})`);
}

export interface DraftSong {
  draftId: string;
  songId: string;
  title: string;
}

/**
 * `n` backlog drafts and their draft Songs. With `rename` each Song is renamed `<tag> <i>` so the Board's text filter
 * (`q=<tag>`) shows exactly them; without it the Songs keep the reconciler's title (cheaper for the 52-draft cap spec).
 * Release them with `archiveDrafts` when the spec is done.
 */
export async function makeDraftSongs(request: APIRequestContext, n: number, tag: string, rename: boolean): Promise<DraftSong[]> {
  const drafts: DraftOut[] = [];
  for (let i = 0; i < n; i++) {
    drafts.push(await apiJSON<DraftOut>(request, "POST", "/api/drafts", { request: recipe(`${tag}-${i}`, 500_000 + Math.floor(Math.random() * 400_000)) }, true));
  }
  const out: DraftSong[] = [];
  for (const [i, d] of drafts.entries()) {
    const s = (await waitSongs(request, `draft_id=${d.id}&include_hidden=true`))[0];
    if (!s) throw new Error("unreachable");
    let title = s.title;
    if (rename) {
      title = `${tag} ${i}`;
      const rev = (await getSong(request, s.id)).song.revision;
      await apiJSON(request, "PATCH", `/api/songs/${s.id}`, { revision: rev, title });
    }
    out.push({ draftId: d.id, songId: s.id, title });
  }
  return out;
}

export async function archiveDrafts(request: APIRequestContext, draftIds: string[]): Promise<void> {
  for (const id of draftIds) {
    const cur = await apiJSON<DraftOut>(request, "GET", `/api/drafts/${id}`);
    await apiJSON(request, "PATCH", `/api/drafts/${id}`, { revision: cur.revision, status: "archived" });
  }
}

/** Sets the angle of a Song (a slug). */
export async function setAngle(request: APIRequestContext, songId: string, angle: string): Promise<void> {
  const rev = (await getSong(request, songId)).song.revision;
  await apiJSON(request, "PATCH", `/api/songs/${songId}`, { revision: rev, angle });
}

export interface ComposedSong {
  songId: string;
  root: string;
  /** Every take id, root first. */
  takes: string[];
}

/**
 * A rendered Song with `takes` takes: one composed root (human confirmation) plus `takes - 1` new-seed children.
 * A take that collides with existing audio in the simulated renderer is made again (m3Api.DUPLICATE_AUDIO).
 */
export async function composeSong(request: APIRequestContext, label: string, takes: number, style = "english, synth pop, 100 bpm"): Promise<ComposedSong> {
  let root = "";
  for (let round = 0; round <= 8 && root === ""; round++) {
    const tag = uniq(label);
    const e = await estimate(request, "/api/generations/estimate", { items: [{ mode: "compose_new", title: tag, recipe: recipe(tag, 100_000 + Math.floor(Math.random() * 1_000_000), style) }] });
    const b = await pollBatch(request, (await createHuman(request, e, "e2e l1 song")).id, allTerminal);
    assertOnlyDuplicateFailures(b);
    const j = first(b.jobs);
    if (j.state === "completed" && j.track_id) root = j.track_id;
  }
  if (root === "") throw new Error("composing an L1 e2e root collided with existing audio too often");
  const kids = takes > 1 ? (await newSeedChildren(request, root, takes - 1)).map((j) => j.track_id ?? "") : [];
  const song = first(await waitSongs(request, `track_id=${root}`));
  const detail = await waitTakes(request, song.id, takes);
  return { songId: song.id, root, takes: [root, ...kids].filter((t) => detail.takes.some((x) => x.id === t)) };
}
