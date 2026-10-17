// Step-up without losing the music (wave 4a, D1). A gated /human/api call that needs a second factor does not unload
// the page: the confirmation opens in a SECOND tab, the player is snapshotted first, and when that tab reports
// completion (BroadcastChannel) or this tab becomes visible and the gate is open, the failed request is retried
// here, in the tab whose player never unloaded. Without a second tab (popup blocked, installed iOS app) it falls
// back to the full-page hop of old; the snapshot makes "Resume at m:ss" work after it.
//
// Nothing here opens a gate: the server still decides every request on its own (the edge's two-factor rule for
// ^/human/ in production, the app's own step-up in local mode). A forged "done" message only causes a retry.
import { writable, type Readable } from "svelte/store";

export const CHANNEL = "mm-stepup";
/** Local mode: the password form, which ends on the done page (it posts on CHANNEL and closes itself). */
export const LOCAL_URL = "/auth/step-up?next=/auth/step-up/done";
/** Proxy mode: a document load of /human/ is what makes the edge ask for the second factor; the tab then reports itself done. */
export const EDGE_URL = "/human/?mm_stepup=1";
/** The cheapest human-only read: a 200 means the gate is open for this browser right now. */
export const GATE_PROBE = "/human/api/devices";

/** local: the app answered 403 STEP_UP_REQUIRED. edge: the edge refused the call (401, or a redirect to its login). */
export type Trigger = "local" | "edge";

export interface StepUpDeps {
  /** window.open from the user's gesture; false when the browser refused (popup blocked). */
  open(url: string): boolean;
  assign(url: string): void;
  /** Path + query of the current page. */
  here(): string;
  /** Save the player (playerStore) before anything that could unload the page. */
  snapshot(): void;
  /** Installed iOS app / standalone display mode: a second tab is not available there. */
  standalone(): boolean;
  /** Subscribes to "the other tab is done" (verify=false) and "this tab is visible again" (verify=true, check the gate). */
  listen(signal: (verify: boolean) => void): () => void;
  gateOpen(): Promise<boolean>;
  /** The gate opened and the request is about to be retried. */
  completed?(): void;
}

export interface StepUpState {
  waiting: Trigger | null;
}

export function isDoneMessage(d: unknown): boolean {
  return typeof d === "object" && d !== null && (d as { type?: unknown }).type === "done";
}

export function createStepUp(deps: StepUpDeps) {
  const state = writable<StepUpState>({ waiting: null });
  let pending: Promise<boolean> | null = null;
  let finish: ((ok: boolean) => void) | null = null;
  let current: Trigger = "local";

  const urlFor = (k: Trigger) => (k === "local" ? LOCAL_URL : EDGE_URL);

  /** The full-page fallback. */
  function hop(kind: Trigger) {
    const here = deps.here();
    deps.assign(kind === "local" ? "/auth/step-up?next=" + encodeURIComponent(here) : here);
  }

  /**
   * Resolves true when the gate is open and the caller should retry its request; false when the user gave up or
   * this page is navigating away. Concurrent callers share one confirmation.
   */
  function require(kind: Trigger): Promise<boolean> {
    if (pending) return pending;
    deps.snapshot();
    if (deps.standalone() || !deps.open(urlFor(kind))) {
      hop(kind);
      return Promise.resolve(false);
    }
    current = kind;
    state.set({ waiting: kind });
    pending = new Promise<boolean>((resolve) => {
      let off = () => {};
      let checking = false;
      const done = (ok: boolean) => {
        off();
        pending = null;
        finish = null;
        state.set({ waiting: null });
        if (ok) deps.completed?.();
        resolve(ok);
      };
      finish = done;
      off = deps.listen((verify) => {
        if (!verify) return done(true);
        if (checking) return;
        checking = true;
        void deps
          .gateOpen()
          .then((open) => {
            if (open) done(true);
          })
          .finally(() => (checking = false));
      });
    });
    return pending;
  }

  return {
    subscribe: state.subscribe as Readable<StepUpState>["subscribe"],
    require,
    /** The user gave up waiting. */
    cancel() {
      finish?.(false);
    },
    /** "I confirmed": re-check the gate now. */
    async check() {
      if (finish && (await deps.gateOpen())) finish(true);
    },
    /** The second tab was closed: open it again (from a click) or take the full-page hop. */
    reopen() {
      if (!pending) return;
      if (!deps.standalone() && deps.open(urlFor(current))) return;
      const f = finish;
      deps.snapshot();
      hop(current);
      f?.(false);
    },
  };
}

export type StepUp = ReturnType<typeof createStepUp>;

/** window.open without noopener: the null a blocked popup returns is how the fallback is detected. */
export function browserDeps(snapshot: () => void = () => {}): StepUpDeps {
  return {
    open(url) {
      try {
        return window.open(url, "_blank") !== null;
      } catch {
        return false;
      }
    },
    assign: (u) => location.assign(u),
    here: () => location.pathname + location.search,
    snapshot: () => snapshot(),
    standalone() {
      const nav = navigator as Navigator & { standalone?: boolean };
      return nav.standalone === true || (typeof matchMedia === "function" && matchMedia("(display-mode: standalone)").matches);
    },
    listen(signal) {
      let ch: BroadcastChannel | null = null;
      try {
        ch = new BroadcastChannel(CHANNEL);
        ch.onmessage = (e: MessageEvent) => {
          if (isDoneMessage(e.data)) signal(false);
        };
      } catch {
        /* no BroadcastChannel: the visibility re-check carries it */
      }
      const vis = () => {
        if (document.visibilityState === "visible") signal(true);
      };
      document.addEventListener("visibilitychange", vis);
      return () => {
        ch?.close();
        document.removeEventListener("visibilitychange", vis);
      };
    },
    async gateOpen() {
      try {
        const r = await fetch(GATE_PROBE, { redirect: "manual", cache: "no-store", credentials: "same-origin" });
        return r.status === 200;
      } catch {
        return false;
      }
    },
    completed() {
      window.dispatchEvent(new CustomEvent("mm:stepup-done"));
    },
  };
}

/** The app-wide flow; App installs the player snapshot into it. */
let snapshotHook: () => void = () => {};
export function setStepUpSnapshot(fn: () => void): void {
  snapshotHook = fn;
}
export const stepUp = createStepUp(browserDeps(() => snapshotHook()));

/**
 * The proxy-mode second tab: a document load of /human/?mm_stepup=1 got past the edge, so the confirmation is done.
 * It reports that and closes; the SPA (and its player) is never mounted in this tab.
 */
export function isStepUpTab(loc: Pick<Location, "pathname" | "search">): boolean {
  return (loc.pathname === "/human" || loc.pathname.startsWith("/human/")) && new URLSearchParams(loc.search).get("mm_stepup") === "1";
}

export function runStepUpTab(doc: Document): void {
  doc.body.textContent = "Confirmed. You can close this tab; the other one continues.";
  try {
    const ch = new BroadcastChannel(CHANNEL);
    ch.postMessage({ type: "done" });
    setTimeout(() => {
      ch.close();
      window.close();
    }, 100);
  } catch {
    /* the other tab's visibility re-check carries it */
  }
}
