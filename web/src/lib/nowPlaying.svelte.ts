// Now-playing sheet state. On a phone the sheet is a history entry: opening pushes {mmSheet:"now"}, so Android Back
// and the iOS edge swipe close the sheet instead of leaving the page, and the router ignores that entry (router.ts).
// On desktop it is a 360 px side panel and takes no history entry.
import { viewport } from "./viewport.svelte";

export const SHEET_STATE = "now";
const OPENER = '[data-testid="player-open-sheet"]';

/** Drag-down closes past 30 % of the sheet height. */
export function dragCloses(dy: number, height: number): boolean {
  return height > 0 && dy > height * 0.3;
}

class Sheet {
  open = $state(false);
  /** The sheet shows its queue view instead of the player. */
  queueView = $state(false);
  /** A history entry belongs to the open sheet. */
  private pushed = false;
  private opener: HTMLElement | null = null;

  show(opener?: HTMLElement | null, queue = false) {
    if (this.open) {
      this.queueView = queue || this.queueView;
      return;
    }
    this.opener = opener ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    this.queueView = queue;
    if (viewport.phone) {
      history.pushState({ mmSheet: SHEET_STATE }, "");
      this.pushed = true;
    }
    this.open = true;
  }

  /** Close from the UI (button, scrim, Escape, drag). With a history entry that is a Back. */
  close() {
    if (!this.open) return;
    if (this.pushed && isSheetState(history.state)) history.back();
    else this.finish();
  }

  /** The route is changing underneath the sheet (a link inside it): drop it without touching the history. */
  dismissForNavigation() {
    this.pushed = false;
    this.open = false;
    this.opener = null;
  }

  /** Navigate from inside the sheet: the sheet's own history entry becomes the destination (one Back returns). */
  navigate(to: string) {
    const had = this.pushed;
    this.dismissForNavigation();
    if (had) history.replaceState(null, "", to);
    else history.pushState(null, "", to);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }

  /** popstate: leaving the sheet entry closes it; arriving at one (Forward) reopens it. */
  onPopState(e: PopStateEvent) {
    if (isSheetState(e.state)) {
      if (viewport.phone && !this.open) {
        this.pushed = true;
        this.opener = null;
        this.open = true;
      }
      return;
    }
    if (this.open) this.finish();
  }

  private finish() {
    this.open = false;
    this.pushed = false;
    const o = this.opener;
    this.opener = null;
    queueMicrotask(() => {
      const target = o && o.isConnected ? o : document.querySelector<HTMLElement>(OPENER);
      target?.focus();
    });
  }
}

export const sheet = new Sheet();

export function isSheetState(s: unknown): boolean {
  return typeof s === "object" && s !== null && (s as { mmSheet?: unknown }).mmSheet === SHEET_STATE;
}

/** Call once at boot: a reload keeps history.state, but the sheet itself is not restored. */
export function installSheet(): () => void {
  if (isSheetState(history.state)) history.replaceState(null, "");
  const on = (e: PopStateEvent) => sheet.onPopState(e);
  window.addEventListener("popstate", on);
  return () => window.removeEventListener("popstate", on);
}
