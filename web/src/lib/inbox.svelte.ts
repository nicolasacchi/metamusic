// Shared inbox state: the seen marker, the heard set and the numbers behind the New tab badge. The badge is refreshed
// on navigation (App) and by the New page itself; the heard tracker watches the player element.
import { api } from "../api/client";
import { approvals } from "./approvals.svelte";
import { getDevice } from "./device.svelte";
import {
  badgeFor,
  clock,
  hasSeenMarker,
  createHeardTracker,
  heardKey,
  countNew,
  readHeard,
  readSeenAt,
  writeHeard,
  writeSeenAt,
} from "./inbox";
import type { Player } from "./player.svelte";

function store(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}
// Storage blocked or absent (private modes, tests): the values last until reload.
const mem: Record<string, string> = {};
const kv = {
  getItem: (k: string) => {
    try {
      return store()?.getItem(k) ?? mem[k] ?? null;
    } catch {
      return mem[k] ?? null;
    }
  },
  setItem: (k: string, v: string) => {
    mem[k] = v;
    store()?.setItem(k, v);
  },
};

function deviceKey(): string {
  try {
    return heardKey(getDevice().deviceId);
  } catch {
    return heardKey("");
  }
}

// First visit on this device: nothing is "new" yet. Without this, opening an existing library would flag a week of takes.
if (!hasSeenMarker(kv)) writeSeenAt(kv, clock());

export const inbox = $state({
  seenAt: readSeenAt(kv, clock()),
  /** New takes after the marker (page 1 of search_tracks, strictly after). */
  newCount: 0,
  duels: 0,
  proposals: 0,
  heard: readHeard(kv, deviceKey()) as Set<string>,
  loaded: false,
});

/** Bumped when the heard set changes, for components that show the dot. */
export const heardVersion = $state({ n: 0 });

export function reloadSeen(): void {
  inbox.seenAt = readSeenAt(kv, clock());
}

export function markAllSeen(now: Date = clock()): void {
  inbox.seenAt = writeSeenAt(kv, now);
  inbox.newCount = 0;
}

export const needsYouCount = (): number => approvals.items.length + inbox.duels + inbox.proposals;
export const badge = (): string | null => badgeFor(inbox.newCount, needsYouCount());

let flight = 0;
/** Cheap counts for the tab badge: one tracks page, two limit-1 lists. Failures keep the previous numbers. */
export async function refreshInboxBadge(): Promise<void> {
  const mine = ++flight;
  reloadSeen();
  const seen = inbox.seenAt;
  const [t, d, s] = await Promise.allSettled([
    api.GET("/api/tracks", { params: { query: { recorded_from: seen, sort: "recorded_asc", limit: 50 } } }),
    api.GET("/api/duels", { params: { query: { state: ["open"], limit: 1 } } }),
    api.GET("/api/songs", { params: { query: { has_open_proposals: true, limit: 1 } } }),
  ]);
  if (mine !== flight) return;
  if (t.status === "fulfilled" && t.value.data) {
    const page = t.value.data;
    inbox.newCount = countNew(page.items, page.total, seen);
  }
  if (d.status === "fulfilled" && d.value.data) inbox.duels = d.value.data.total;
  if (s.status === "fulfilled" && s.value.data) inbox.proposals = s.value.data.total;
  inbox.loaded = true;
}

/** Marks takes heard from the player's own position samples (device-local). Returns cleanup. */
export function installHeard(player: Player): () => void {
  const el = player.el;
  if (!el) return () => {};
  const key = deviceKey();
  const tracker = createHeardTracker((id) => {
    inbox.heard.add(id);
    heardVersion.n++;
    writeHeard(kv, key, inbox.heard);
  }, inbox.heard);
  const sample = () => tracker.sample(player.state.trackId, el.currentTime, Number.isFinite(el.duration) ? el.duration : player.state.duration, !el.paused);
  el.addEventListener("timeupdate", sample);
  return () => el.removeEventListener("timeupdate", sample);
}
