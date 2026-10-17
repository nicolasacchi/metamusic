// New inbox v1 (wave 3b): a pure model over existing ops only. The seen marker and the heard set live in localStorage
// (device-local; a server-side marker is a later gap). Nothing here touches the DOM or the network.
import type { components } from "../api/schema";

type Track = components["schemas"]["TrackSummary"];
type Job = components["schemas"]["JobSummary"];
type Batch = components["schemas"]["Batch"];

export const SEEN_KEY = "mm:inbox-seen-at";
export const HEARD_KEY_PREFIX = "mm:inbox-heard:";
/** With no marker yet, "new" means the last week. */
export const DEFAULT_WINDOW_MS = 7 * 24 * 3600 * 1000;
/** Heard: at least this share of the take, or this many seconds, actually played. */
export const HEARD_SHARE = 0.5;
export const HEARD_SECONDS = 60;
export const HEARD_CAP = 1000;

export interface KV {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
}

/** The current time (kept here so reactive modules need not construct a Date). */
export const clock = (): Date => new Date();

// ---- seen marker

/** The marker as an ISO time; absent or unparsable -> a week before `now`. */
export function readSeenAt(store: KV, now: Date): string {
  try {
    const v = store.getItem(SEEN_KEY);
    if (v && Number.isFinite(Date.parse(v))) return v;
  } catch {
    // storage blocked: behave as if unset
  }
  return new Date(now.getTime() - DEFAULT_WINDOW_MS).toISOString();
}

export function hasSeenMarker(store: KV): boolean {
  try {
    const v = store.getItem(SEEN_KEY);
    return !!v && Number.isFinite(Date.parse(v));
  } catch {
    return false;
  }
}

export function writeSeenAt(store: KV, at: Date): string {
  const iso = at.toISOString();
  try {
    store.setItem(SEEN_KEY, iso);
  } catch {
    // storage blocked: the marker lasts until reload
  }
  return iso;
}

/** Takes strictly after the marker, in creation order (oldest first), ties by id. */
export function newSince(tracks: readonly Track[], seenAt: string): Track[] {
  const cut = Date.parse(seenAt);
  return tracks
    .filter((t) => Date.parse(t.recorded_at) > cut)
    .sort((a, b) => Date.parse(a.recorded_at) - Date.parse(b.recorded_at) || (a.id < b.id ? -1 : 1));
}

/**
 * How many takes are strictly after the marker. `total` is the server's count for recorded_from=<marker> (inclusive), so
 * the loaded items that are not after it (the boundary take) are taken off.
 */
export function countNew(loaded: readonly Track[], total: number, seenAt: string): number {
  const fresh = newSince(loaded, seenAt).length;
  return Math.max(fresh, total - (loaded.length - fresh));
}

// ---- heard set

export const heardKey = (deviceId: string): string => HEARD_KEY_PREFIX + (deviceId || "local");

export function readHeard(store: KV, key: string): Set<string> {
  try {
    const raw = store.getItem(key);
    const v: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

export function writeHeard(store: KV, key: string, heard: ReadonlySet<string>): void {
  const ids = [...heard].slice(-HEARD_CAP);
  try {
    store.setItem(key, JSON.stringify(ids));
  } catch {
    // storage blocked or full
  }
}

/** The rule: at least half the take or 60 s of it played. */
export function isHeard(playedS: number, durationS: number): boolean {
  if (!(playedS > 0)) return false;
  if (playedS >= HEARD_SECONDS) return true;
  return durationS > 0 && playedS >= HEARD_SHARE * durationS;
}

/** A timeupdate gap larger than this is a seek or a stall, not listening. */
const MAX_STEP_S = 2;

/**
 * Accumulates listening time per take from player position samples. Only forward steps of up to 2 s count, so seeking
 * to the end or letting the tab idle does not mark a take heard. `sample` returns the id when the take just became heard.
 */
export function createHeardTracker(onHeard: (id: string) => void, already: ReadonlySet<string> = new Set()) {
  const heard = new Set(already);
  let track = "";
  let last = -1;
  let played = 0;
  return {
    sample(trackId: string, positionS: number, durationS: number, playing: boolean): void {
      if (trackId !== track) {
        track = trackId;
        last = -1;
        played = 0;
      }
      if (!playing || !trackId) {
        last = -1;
        return;
      }
      if (last >= 0) {
        const d = positionS - last;
        if (d > 0 && d <= MAX_STEP_S) played += d;
      }
      last = positionS;
      if (!heard.has(trackId) && isHeard(played, durationS)) {
        heard.add(trackId);
        onHeard(trackId);
      }
    },
    has: (id: string) => heard.has(id),
    all: () => heard as ReadonlySet<string>,
  };
}

// ---- sections

export interface Row {
  track: Track;
  /** The dot: not heard yet. */
  unheard: boolean;
}

export type SegmentState = "done" | "running" | "queued" | "failed";
export interface Segment {
  jobId: string;
  state: SegmentState;
  trackId: string | null;
}
export interface FlightBatch {
  id: string;
  title: string;
  segments: Segment[];
  ready: number;
  generating: number;
  queued: number;
  failed: number;
  /** All takes done (completed, failed or cancelled) but the batch row has not caught up. */
  settled: boolean;
}

const RUNNING = new Set(["submitting", "starting", "generating", "transferring", "ingesting", "cancelling", "failing"]);

export function segmentState(j: Pick<Job, "state">): SegmentState {
  if (j.state === "completed") return "done";
  if (RUNNING.has(j.state)) return "running";
  if (j.state === "failed" || j.state === "cancelled" || j.state === "expired") return "failed";
  return "queued";
}

export function flightBatch(b: Batch): FlightBatch {
  const jobs = [...b.jobs].sort((x, y) => x.take_index - y.take_index);
  const segments = jobs.map((j) => ({ jobId: j.id, state: segmentState(j), trackId: j.track_id }));
  const n = (s: SegmentState) => segments.filter((x) => x.state === s).length;
  const title = jobs.find((j) => j.title)?.title ?? "Batch";
  return {
    id: b.id,
    title,
    segments,
    ready: n("done"),
    generating: n("running"),
    queued: n("queued"),
    failed: n("failed"),
    settled: segments.length > 0 && n("running") + n("queued") === 0,
  };
}

export function flightSummary(f: FlightBatch): string {
  const parts = [`${f.ready} ready`];
  if (f.generating) parts.push(`${f.generating} generating`);
  if (f.queued) parts.push(`${f.queued} queued`);
  if (f.failed) parts.push(`${f.failed} failed`);
  return parts.join(" · ");
}

/** The job whose state change to wait for: the one furthest along. */
export function soonestJob(batches: readonly Batch[]): Job | null {
  const order = ["ingesting", "transferring", "generating", "starting", "submitting", "queued"];
  let best: Job | null = null;
  let rank = Infinity;
  for (const b of batches)
    for (const j of b.jobs) {
      const r = order.indexOf(j.state);
      if (r >= 0 && r < rank) {
        best = j;
        rank = r;
      }
    }
  return best;
}

/** Jobs that completed between two views of the same batches. */
export function newlyCompleted(before: readonly Batch[], after: readonly Batch[]): Job[] {
  const was = new Map<string, string>();
  for (const b of before) for (const j of b.jobs) was.set(j.id, j.state);
  const out: Job[] = [];
  for (const b of after)
    for (const j of b.jobs) {
      const prev = was.get(j.id);
      if (j.state === "completed" && j.track_id && prev !== undefined && prev !== "completed") out.push(j);
    }
  return out;
}

export interface InboxInput {
  /** search_tracks recorded_from=<seen>, any order; filtered and ordered here. */
  tracks: readonly Track[];
  /** total reported by the server for the same query (may exceed tracks.length). */
  trackTotal: number;
  seenAt: string;
  heard: ReadonlySet<string>;
  /** unrated drafts, the v1 proxy for "never heard". */
  unheardProxy: readonly Track[];
  batches: readonly Batch[];
  approvals: number;
  openDuels: number;
  proposals: number;
  failures: readonly Job[];
}

export interface InboxModel {
  count: number;
  /** Sum of the loaded takes' durations, seconds. */
  seconds: number;
  /** More new takes exist than were loaded. */
  partial: boolean;
  rows: Row[];
  stillUnheard: Row[];
  inFlight: FlightBatch[];
  needsYou: { approvals: number; duels: number; proposals: number; total: number };
  failures: Job[];
  /** Play-all-new order (ids). */
  playOrder: string[];
}

export function buildInbox(i: InboxInput): InboxModel {
  const fresh = newSince(i.tracks, i.seenAt);
  const rows = fresh.map((track) => ({ track, unheard: !i.heard.has(track.id) }));
  const inNew = new Set(fresh.map((t) => t.id));
  const stillUnheard = i.unheardProxy
    .filter((t) => !inNew.has(t.id) && !i.heard.has(t.id))
    .map((track) => ({ track, unheard: true }));
  const cut = Date.parse(i.seenAt);
  const failures = i.failures.filter((j) => j.state === "failed" && Date.parse(j.updated_at) > cut);
  const needs = { approvals: i.approvals, duels: i.openDuels, proposals: i.proposals, total: i.approvals + i.openDuels + i.proposals };
  return {
    count: countNew(i.tracks, i.trackTotal, i.seenAt),
    seconds: fresh.reduce((s, t) => s + (t.duration_s > 0 ? t.duration_s : 0), 0),
    partial: i.trackTotal > i.tracks.length,
    rows,
    stillUnheard,
    inFlight: i.batches.filter((b) => b.state === "active").map(flightBatch),
    needsYou: needs,
    failures,
    playOrder: fresh.map((t) => t.id),
  };
}

// ---- badge

/** Unseen new takes plus needs-you items; "9+" past nine, null (no badge) for zero. */
export function badgeFor(newTakes: number, needsYou: number): string | null {
  const n = Math.max(0, newTakes) + Math.max(0, needsYou);
  if (n <= 0) return null;
  return n > 9 ? "9+" : String(n);
}

// ---- labels

const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const pad = (n: number) => String(n).padStart(2, "0");

/** "Tue 14:02" within the last week, else "12 Oct 14:02" (local time). */
export function sinceLabel(iso: string, now: Date): string {
  const d = new Date(iso);
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  if (now.getTime() - d.getTime() < 6 * 24 * 3600 * 1000) return `${DAYS[d.getDay()]} ${hm}`;
  const mon = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"][d.getMonth()];
  return `${d.getDate()} ${mon} ${hm}`;
}

export const sourceLabel = (seenAt: string, now: Date): string => `New since ${sinceLabel(seenAt, now)}`;

export function minutesLabel(seconds: number): string {
  if (seconds > 0 && seconds < 30) return "<1 min";
  const m = Math.round(seconds / 60);
  return `${m} min`;
}

export function headerLine(m: Pick<InboxModel, "count" | "seconds" | "partial">, seenAt: string, now: Date): string {
  const n = `${m.count} new take${m.count === 1 ? "" : "s"}`;
  const mins = `${m.partial ? "at least " : ""}${minutesLabel(m.seconds)}`;
  return `Since ${sinceLabel(seenAt, now)} · ${n} · ${mins}`;
}

/** "2 h", "3 d", "just now". */
export function ageShort(iso: string, now: Date): string {
  const s = Math.max(0, (now.getTime() - Date.parse(iso)) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  if (s < 86400) return `${Math.floor(s / 3600)} h`;
  return `${Math.floor(s / 86400)} d`;
}
