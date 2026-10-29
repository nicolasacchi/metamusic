// Plays a Record through the one player (L2-W3, C131.5). Stable entry points for the Records pages:
//   playRecord(view, { from? })            replace the queue with the Record's resolved Takes, plan gains applied
//   enqueueRecord(view)                    append them ("Add to queue"); starts paused when nothing is loaded
//   playAsTape(recordId, revision, opts?)  play the lossless tape of that revision, exporting one first when none is ready
//   seekToChapter(tape, index)             seek the playing tape to a chapter
// Call them from a click: the player needs the user gesture for autoplay (a tape that had to be exported
// starts after the wait, so the mini-player may ask for one tap, "Tap to continue").
import { writable } from "svelte/store";
import { api } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { components } from "../api/schema";
import { player as sharedPlayer, type Player } from "./player.svelte";
import { pushToast } from "./toast.svelte";
import { gainMap, recordHref, rememberTapeTitle, tapeItem, takeQueue, type RecordView, type Tape, type TapeChapter } from "./recordMedia";

export { gainMap, recordHref, recordIdOfHref, takeQueue, tapeItem, isTapeItem } from "./recordMedia";

type TapeBrief = components["schemas"]["TapeBrief"];

/** Poll policy of a queued or running export (C131.2): every second, at most 15 minutes. */
export const POLL_MS = 1000;
export const POLL_MAX_MS = 15 * 60_000;
/** How many ready tapes are opened to find one at the current revision (newest first). */
const MAX_PROBES = 3;

export type ExportResult = { tape: Tape } | { status: number; code: string; message: string };

/** The network and clock behind playAsTape; tests replace it. All calls go through the generated client. */
export interface TapeEnv {
  player: Pick<Player, "play" | "seek" | "load" | "enqueue" | "mergeGains" | "state">;
  tape(id: string): Promise<Tape | null>;
  /** The Record's tape list (get_record, tapes only); null when it cannot be read. */
  tapes(recordId: string): Promise<{ title: string; tapes: TapeBrief[] } | null>;
  exportTape(recordId: string, revision: number, key: string): Promise<ExportResult>;
  sleep(ms: number): Promise<void>;
  now(): number;
}

const defaultEnv: TapeEnv = {
  player: sharedPlayer,
  async tape(id) {
    try {
      return (await api.GET("/api/tapes/{id}", { params: { path: { id } } })).data ?? null;
    } catch {
      return null;
    }
  },
  async tapes(recordId) {
    try {
      const r = await api.GET("/api/records/{id}", { params: { path: { id: recordId }, query: { include: ["tapes"] } } });
      return r.data ? { title: r.data.record.title, tapes: r.data.tapes ?? [] } : null;
    } catch {
      return null;
    }
  },
  async exportTape(recordId, revision, key) {
    try {
      const r = await api.POST("/api/records/{id}/tapes", { params: { path: { id: recordId }, header: { "Idempotency-Key": key } }, body: { revision } });
      if (r.data) return { tape: r.data };
      const e = r.error as { code?: string; message?: string } | undefined;
      return { status: r.response.status, code: e?.code ?? "ERROR", message: e?.message ?? "The tape export failed" };
    } catch {
      return { status: 0, code: "NETWORK", message: "Could not reach the server" };
    }
  },
  sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
  now: () => Date.now(),
};

/** The tape the app last started (for a chapter list under the player); consumers also check the player's current item. */
export const activeTape = writable<{ tape: Tape; recordId: string; title: string; names: Record<string, string> } | null>(null);

function sourceOf(recordId: string, title: string) {
  return { label: title.trim() || "Record", href: recordHref(recordId) };
}

/** Replace the queue with the Record's resolved Takes (empty slots skipped) and its plan gains. false: nothing playable. */
export function playRecord(view: RecordView, opts: { from?: string } = {}, p: Pick<Player, "play"> = sharedPlayer): boolean {
  const queue = takeQueue(view);
  const first = opts.from && queue.includes(opts.from) ? opts.from : queue[0];
  if (first === undefined) return false;
  const t = view.slots.find((s) => s.take?.id === first)?.take;
  p.play(first, queue, {
    ...(t ? { title: t.title } : {}),
    album: view.record.title,
    source: sourceOf(view.record.id, view.record.title),
    undo: true,
    gains: gainMap(view),
  });
  return true;
}

/** "Add to queue": append the Record's Takes with their gains. Nothing loaded yet: load them paused as the queue. */
export function enqueueRecord(view: RecordView, p: Pick<Player, "load" | "enqueue" | "mergeGains" | "state"> = sharedPlayer): boolean {
  const queue = takeQueue(view);
  if (queue.length === 0) return false;
  if (!p.state.trackId) {
    p.load(queue[0]!, queue, { album: view.record.title, source: sourceOf(view.record.id, view.record.title), gains: gainMap(view) });
  } else {
    for (const id of queue) p.enqueue(id, true);
    p.mergeGains(gainMap(view));
  }
  pushToast(queue.length === 1 ? "Added 1 take to the queue" : `Added ${queue.length} takes to the queue`, { kind: "info" });
  return true;
}

export type TapeOutcome =
  | { kind: "playing"; tape: Tape }
  /** Not started: `wait` was false, the wait was aborted, or the export is still running after the poll limit. */
  | { kind: "pending"; tape: Tape }
  /** `code` is the server's (STALE_REVISION, RECORD_NOT_PUBLISHED, TAPE_NEEDS_REVIEW, TAPE_QUOTA, ...), NOT_IMPLEMENTED while the tape service is unbuilt (501). */
  | { kind: "failed"; code: string; message: string; tape?: Tape };

export interface PlayAsTapeOptions {
  /** The Record's title (queue source label and mini-player title); fetched with the tape list when absent. */
  title?: string;
  /** Tape briefs already at hand (RecordView.tapes); absent: read from get_record. */
  tapes?: TapeBrief[];
  /** Slot id -> chapter name for the chapter list (see chapterNames). */
  names?: Record<string, string>;
  /** false: return `pending` instead of waiting for a queued or running export. Default true. */
  wait?: boolean;
  signal?: AbortSignal;
  /** Each poll of a queued or running export (phase and progress text). */
  onProgress?(t: Tape): void;
}

const ready = (t: Tape | null): boolean => t !== null && t.state === "ready" && t.download !== null;

/** Slot id -> "Song title" for the chapter list, from the Record the tape was cut from. */
export function chapterNames(view: Pick<RecordView, "slots">): Record<string, string> {
  const out: Record<string, string> = {};
  for (const s of view.slots) out[s.id] = s.take?.title ?? s.song.title;
  return out;
}

/**
 * Play the Record as one lossless tape. A ready tape cut from exactly `revision` is reused; otherwise an export is
 * requested (the server reuses an identical live one) and, by default, awaited. Handles the tape service being absent.
 */
export async function playAsTape(recordId: string, revision: number, opts: PlayAsTapeOptions = {}, env: TapeEnv = defaultEnv): Promise<TapeOutcome> {
  let title = opts.title ?? "";
  let briefs = opts.tapes;
  if (briefs === undefined || !title) {
    const got = await env.tapes(recordId);
    if (got) {
      briefs ??= got.tapes;
      title ||= got.title;
    }
  }
  const candidates = (briefs ?? [])
    .filter((b) => b.state === "ready")
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .slice(0, MAX_PROBES);
  for (const b of candidates) {
    const t = await env.tape(b.id);
    if (t && ready(t) && t.record_revision === revision) return start(t);
  }

  const send = idempotentAction((key) => env.exportTape(recordId, revision, key));
  const res = await send();
  if (!("tape" in res)) {
    return {
      kind: "failed",
      code: res.status === 501 ? "NOT_IMPLEMENTED" : res.code,
      message: res.status === 501 ? "Tape export is not available yet" : res.message,
    };
  }
  let tape = res.tape;
  const t0 = env.now();
  while (!ready(tape)) {
    if (tape.state === "failed" || tape.state === "expired") {
      return { kind: "failed", code: tape.error?.code ?? "TAPE_" + tape.state.toUpperCase(), message: tape.error?.message ?? `The tape ${tape.state}`, tape };
    }
    if (opts.wait === false || opts.signal?.aborted || env.now() - t0 >= POLL_MAX_MS) return { kind: "pending", tape };
    opts.onProgress?.(tape);
    await env.sleep(POLL_MS);
    if (opts.signal?.aborted) return { kind: "pending", tape };
    const next = await env.tape(tape.id);
    if (next) tape = next;
  }
  return start(tape);

  function start(t: Tape): TapeOutcome {
    const item = tapeItem(t.id);
    if (title) rememberTapeTitle(t.id, title);
    activeTape.set({ tape: t, recordId, title, names: opts.names ?? {} });
    env.player.play(item, [item], {
      ...(title ? { title } : {}),
      album: "Tape",
      source: sourceOf(recordId, title),
      undo: true,
    });
    return { kind: "playing", tape: t };
  }
}

/** start_ms of chapter `index`, or null when the tape has no such chapter. */
export function chapterStartMs(chapters: readonly Pick<TapeChapter, "index" | "start_ms">[] | null | undefined, index: number): number | null {
  return chapters?.find((c) => c.index === index)?.start_ms ?? null;
}

/** The chapter playing at `positionMs` (the last one that has started), or -1 before the first. */
export function chapterAt(chapters: readonly Pick<TapeChapter, "index" | "start_ms">[] | null | undefined, positionMs: number): number {
  let at = -1;
  let from = -1;
  for (const c of chapters ?? []) {
    if (c.start_ms <= positionMs && c.start_ms >= from) {
      at = c.index;
      from = c.start_ms;
    }
  }
  return at;
}

/** Seek the playing tape to a chapter. false: that tape is not the current item, or there is no such chapter. */
export function seekToChapter(tape: Pick<Tape, "id" | "chapters">, index: number, p: Pick<Player, "seek" | "state"> = sharedPlayer): boolean {
  const ms = chapterStartMs(tape.chapters, index);
  if (ms === null || p.state.trackId !== tapeItem(tape.id)) return false;
  p.seek(ms / 1000);
  return true;
}
