// Player persistence (D2): the queue, position and source survive a reload, a tab kill or a bfcache eviction.
// Only ids and numbers are stored: never a URL, token or title (links are re-minted on resume, titles re-fetched).
// A restore is always paused: browsers refuse audio at boot, so one tap on "Resume <title> at m:ss" continues.

export const STORAGE_KEY = "mm:player:v1";
export const VERSION = 1;
export const MAX_QUEUE = 500;
export const TTL_MS = 24 * 3600_000;
export const SAVE_EVERY_MS = 5000;
/** Entries kept before the current one when the queue has to be cut to MAX_QUEUE. */
const KEEP_BEHIND = 50;

export interface SavedSource {
  label: string;
  href: string;
}

export interface SavedPlayer {
  v: 1;
  queue: string[];
  index: number;
  queuedNext: number;
  /** Seconds into the current track. */
  position: number;
  trackId: string;
  source: SavedSource | null;
  /** epoch ms */
  savedAt: number;
}

/** What the player hands over to be saved (no version/time: the store stamps those). */
export type PlayerSnapshot = Omit<SavedPlayer, "v" | "savedAt">;

export interface KV {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
  removeItem(k: string): void;
}

const ID = /^[A-Za-z0-9_-]{1,64}$/;

/** Track ids are short opaque tokens: anything with a scheme, slash, dot or query is not one. */
export function isTrackId(s: unknown): s is string {
  return typeof s === "string" && ID.test(s);
}

/** An app-internal path only ("/new", "/?status=draft"): never an absolute URL. */
export function cleanSource(s: unknown): SavedSource | null {
  if (typeof s !== "object" || s === null) return null;
  const { label, href } = s as Record<string, unknown>;
  if (typeof label !== "string" || typeof href !== "string") return null;
  if (!href.startsWith("/") || href.startsWith("//") || href.length > 200 || /[\s\\]/.test(href) || href.includes("://")) return null;
  const l = label.trim().slice(0, 80);
  return l ? { label: l, href } : null;
}

/** Cuts a queue to MAX_QUEUE ids keeping the current one (history first, then the upcoming entries). */
export function capQueue(queue: readonly string[], index: number, queuedNext: number): { queue: string[]; index: number; queuedNext: number } {
  if (queue.length <= MAX_QUEUE) return { queue: [...queue], index, queuedNext };
  const from = Math.max(0, Math.min(index - KEEP_BEHIND, queue.length - MAX_QUEUE));
  const q = queue.slice(from, from + MAX_QUEUE);
  const i = index - from;
  return { queue: q, index: i, queuedNext: Math.max(0, Math.min(queuedNext, q.length - 1 - i)) };
}

export function serialize(s: PlayerSnapshot, now: number): string | null {
  if (!isTrackId(s.trackId)) return null;
  const queue = s.queue.filter(isTrackId);
  // Dropping ids shifts the index: find the track again.
  const at = queue.indexOf(s.trackId);
  if (at < 0) return null;
  const capped = capQueue(queue, at, s.queuedNext);
  const out: SavedPlayer = {
    v: VERSION,
    queue: capped.queue,
    index: capped.index,
    queuedNext: Math.max(0, Math.floor(capped.queuedNext) || 0),
    position: Number.isFinite(s.position) && s.position > 0 ? Math.round(s.position * 10) / 10 : 0,
    trackId: s.trackId,
    source: cleanSource(s.source),
    savedAt: now,
  };
  return JSON.stringify(out);
}

/** Parses a stored value; null for anything stale, foreign, malformed or from another version. */
export function parse(raw: string | null, now: number): SavedPlayer | null {
  if (!raw) return null;
  let o: unknown;
  try {
    o = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof o !== "object" || o === null) return null;
  const r = o as Record<string, unknown>;
  if (r.v !== VERSION) return null;
  const savedAt = r.savedAt;
  if (typeof savedAt !== "number" || !Number.isFinite(savedAt)) return null;
  const age = now - savedAt;
  // A saved time in the future (clock change) is as untrustworthy as an old one.
  if (age < -60_000 || age >= TTL_MS) return null;
  if (!Array.isArray(r.queue) || !r.queue.every(isTrackId) || r.queue.length === 0 || r.queue.length > MAX_QUEUE) return null;
  const queue = r.queue as string[];
  if (!isTrackId(r.trackId)) return null;
  const at = queue.indexOf(r.trackId);
  if (at < 0) return null;
  const qn = typeof r.queuedNext === "number" && Number.isFinite(r.queuedNext) ? Math.floor(r.queuedNext) : 0;
  const pos = typeof r.position === "number" && Number.isFinite(r.position) && r.position > 0 ? r.position : 0;
  return {
    v: VERSION,
    queue,
    index: at,
    queuedNext: Math.max(0, Math.min(qn, queue.length - 1 - at)),
    position: pos,
    trackId: r.trackId,
    source: cleanSource(r.source),
    savedAt,
  };
}

export function save(kv: KV, s: PlayerSnapshot, now: number): boolean {
  const raw = serialize(s, now);
  if (raw === null) return false;
  try {
    kv.setItem(STORAGE_KEY, raw);
    return true;
  } catch {
    return false; // private mode or quota: persistence is best-effort
  }
}

export function load(kv: KV, now: number): SavedPlayer | null {
  try {
    const v = parse(kv.getItem(STORAGE_KEY), now);
    if (v === null) kv.removeItem(STORAGE_KEY);
    return v;
  } catch {
    return null;
  }
}

export function clear(kv: KV): void {
  try {
    kv.removeItem(STORAGE_KEY);
  } catch {
    /* best-effort */
  }
}

/** What installPersistence needs from the player. */
export interface Persisted {
  el: HTMLElement | null;
  state: { trackId: string; playing: boolean; restored: number | null };
  snapshot(): PlayerSnapshot;
}

export interface PersistEnv {
  kv: KV;
  doc: Pick<Document, "addEventListener" | "removeEventListener" | "visibilityState">;
  win: Pick<Window, "addEventListener" | "removeEventListener">;
  now(): number;
  setInterval(fn: () => void, ms: number): unknown;
  clearInterval(h: unknown): void;
}

export function persistEnv(): PersistEnv {
  return {
    kv: localStorage,
    doc: document,
    win: window,
    now: () => Date.now(),
    setInterval: (fn, ms) => setInterval(fn, ms),
    clearInterval: (h) => clearInterval(h as ReturnType<typeof setInterval>),
  };
}

/** Saves the player right now (same rule as the automatic saves). For the moments the page may be about to unload. */
export function persistNow(player: Persisted, env: PersistEnv = persistEnv()): void {
  // A restored, not yet resumed player keeps its original save time: looking at it must not extend the 24 h.
  if (player.state.trackId && player.state.restored === null) save(env.kv, player.snapshot(), env.now());
}

/**
 * Saves on track change / queue edit (mm:state; a position-only change is the interval's job), pause, visibilitychange:hidden, pagehide, and every 5 s while
 * playing. Returns a cleanup. Call after the boot restore so an empty player never overwrites a saved queue.
 */
export function installPersistence(player: Persisted, env: PersistEnv = persistEnv()): () => void {
  const el = player.el;
  if (!el) return () => {};
  const write = () => persistNow(player, env);
  const onHidden = () => {
    if (env.doc.visibilityState === "hidden") write();
  };
  let timer: unknown = null;
  const arm = () => {
    if (timer === null) timer = env.setInterval(() => (player.state.playing ? write() : undefined), SAVE_EVERY_MS);
  };
  arm();
  el.addEventListener("mm:state", write);
  el.addEventListener("pause", write);
  env.doc.addEventListener("visibilitychange", onHidden);
  env.win.addEventListener("pagehide", write);
  return () => {
    el.removeEventListener("mm:state", write);
    el.removeEventListener("pause", write);
    env.doc.removeEventListener("visibilitychange", onHidden);
    env.win.removeEventListener("pagehide", write);
    if (timer !== null) env.clearInterval(timer);
    timer = null;
  };
}
