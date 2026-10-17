// Scroll restoration (P0-9). The browser's own restoration runs before an SPA route has its data, so the router takes
// over (`history.scrollRestoration = "manual"`): every history entry carries a key and its last scrollY in
// `history.state`; Back restores the saved position once the route says its first data has loaded
// (`routeReady()` -> the `mm:route-ready` event); a new entry starts at the top.
//
// Entries that carry `mmSheet` (the now-playing sheet pushes one) are never touched: no key, no saved position.

import { tick } from "svelte";

export const READY_EVENT = "mm:route-ready";
/** Routes that call routeReady() after their first load; Back waits for it (up to READY_TIMEOUT_MS). */
export const READY_ROUTES: ReadonlySet<string> = new Set(["home", "new", "backlog", "board", "song"]);
export const READY_TIMEOUT_MS = 4000;
/** Routes without the signal restore after this short delay instead. */
export const FALLBACK_DELAY_MS = 300;
const SAVE_DEBOUNCE_MS = 150;

interface EntryState {
  mmKey?: string;
  mmScroll?: number;
  mmSheet?: unknown;
}

export interface ScrollDeps {
  history: { state: unknown; replaceState(state: unknown, unused: string): void };
  scrollY(): number;
  scrollTo(y: number): void;
  setTimer(fn: () => void, ms: number): unknown;
  clearTimer(h: unknown): void;
  newKey(): string;
}

function entry(s: unknown): EntryState {
  return s && typeof s === "object" ? (s as EntryState) : {};
}

export const isSheetEntry = (s: unknown): boolean => entry(s).mmSheet !== undefined && entry(s).mmSheet !== null && entry(s).mmSheet !== false;

export function createScrollKeeper(d: ScrollDeps) {
  let saveTimer: unknown = null;
  let pending: { y: number; timer: unknown } | null = null;

  function write(patch: EntryState) {
    const cur = d.history.state;
    if (isSheetEntry(cur)) return;
    const base = cur && typeof cur === "object" ? (cur as Record<string, unknown>) : {};
    try {
      d.history.replaceState({ ...base, ...patch }, "");
    } catch {
      // Safari rate-limits replaceState; a lost save only costs one restore.
    }
  }
  function cancelSave() {
    if (saveTimer !== null) d.clearTimer(saveTimer);
    saveTimer = null;
  }
  function settle() {
    if (!pending) return;
    d.clearTimer(pending.timer);
    const y = pending.y;
    pending = null;
    d.scrollTo(y);
  }

  const keeper = {
    /** Give the current entry a key (first load, or an entry another module pushed). */
    adopt() {
      const e = entry(d.history.state);
      if (isSheetEntry(e) || e.mmKey) return;
      write({ mmKey: d.newKey() });
    },
    /** A scroll event: save the position (debounced), unless a restore is in flight. */
    onScroll() {
      if (pending || isSheetEntry(d.history.state)) return;
      cancelSave();
      saveTimer = d.setTimer(() => {
        saveTimer = null;
        keeper.saveNow();
      }, SAVE_DEBOUNCE_MS);
    },
    saveNow() {
      cancelSave();
      if (pending) return;
      write({ mmScroll: Math.round(d.scrollY()) });
    },
    /** Called before a push: save where we are; returns the state of the entry about to be pushed. */
    leave(): EntryState {
      keeper.saveNow();
      return { mmKey: d.newKey() };
    },
    /** A new entry (push) has been made: start at the top. */
    arrivedNew() {
      cancelSave();
      pending = null;
      d.scrollTo(0);
    },
    /**
     * Back/Forward landed on an entry. Returns false for a sheet entry (the caller must leave it alone).
     * `wired`: the destination route calls routeReady(); otherwise restore shortly after render.
     */
    arrivedPopped(wired: boolean): boolean {
      cancelSave();
      const state = d.history.state;
      if (isSheetEntry(state)) return false;
      if (pending) {
        d.clearTimer(pending.timer);
        pending = null;
      }
      const y = entry(state).mmScroll;
      if (typeof y !== "number") return true;
      const timer = d.setTimer(settle, wired ? READY_TIMEOUT_MS : FALLBACK_DELAY_MS);
      pending = { y, timer };
      return true;
    },
    /** The route finished its first data load. */
    ready() {
      settle();
    },
    /** The user scrolled by hand while a restore waited: honour that and drop the restore. */
    abandon() {
      if (!pending) return;
      d.clearTimer(pending.timer);
      pending = null;
    },
    get restoring() {
      return pending !== null;
    },
  };
  return keeper;
}

/** Called by a route after its first data load has rendered. Safe to call any number of times. */
export async function routeReady(): Promise<void> {
  await tick();
  if (typeof window !== "undefined") window.dispatchEvent(new Event(READY_EVENT));
}
