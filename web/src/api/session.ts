// Session-loss detection (C22.4). Lives in src/api/ because it needs the network.
import { writable } from "svelte/store";

export type Banner = "none" | "expired" | "offline" | "connection";

export interface SessionDeps {
  /** Used for the confirmation probe only. */
  fetch?: (input: string, init: RequestInit) => Promise<Response>;
  isPlaying?: () => boolean;
  reload?: () => void;
  storage?: Pick<Storage, "getItem" | "setItem">;
  now?: () => number;
  onLine?: () => boolean;
  sleep?: (ms: number) => Promise<void>;
}

const RELOADS_KEY = "mm:auth-reloads";
const GUARD_RECENT_MS = 30_000;
const GUARD_WINDOW_MS = 10 * 60_000;
const GUARD_MAX = 3;
const RETRY_DELAY_MS = 1000;

export const IDEMPOTENT = new Set(["GET", "HEAD", "PUT"]);

export function createSession(deps: SessionDeps = {}) {
  const banner = writable<Banner>("none");
  const cfg = { ...deps };
  const now = () => (cfg.now ?? Date.now)();
  const storage = () =>
    cfg.storage ?? (typeof sessionStorage === "undefined" ? undefined : sessionStorage);

  function readReloads(): number[] {
    try {
      const v: unknown = JSON.parse(storage()?.getItem(RELOADS_KEY) ?? "[]");
      return Array.isArray(v) ? v.filter((x): x is number => typeof x === "number") : [];
    } catch {
      return [];
    }
  }

  /** Authentication is confirmed lost: banner while playing, else reload (loop-guarded). */
  function confirmed() {
    if (cfg.isPlaying?.()) {
      banner.set("expired");
      return;
    }
    const t = now();
    const all = readReloads().filter((x) => t - x < GUARD_WINDOW_MS);
    if (all.some((x) => t - x < GUARD_RECENT_MS) || all.length >= GUARD_MAX) {
      banner.set("expired");
      return;
    }
    try {
      storage()?.setItem(RELOADS_KEY, JSON.stringify([...all, t]));
    } catch {
      /* storage unavailable: fall back to the banner rather than risk a loop */
      banner.set("expired");
      return;
    }
    (cfg.reload ?? (() => location.reload()))();
  }

  /** Confirmation probe after repeated network failures. Returns true when auth is lost. */
  async function probe(): Promise<boolean> {
    const f = cfg.fetch ?? ((input, init) => globalThis.fetch(input, init));
    try {
      const r = await f("/api/ping", { redirect: "manual", cache: "no-store", credentials: "same-origin" });
      return r.status === 401 || r.type === "opaqueredirect";
    } catch {
      return false;
    }
  }

  /** A second network failure for a request (after the optional retry). */
  async function networkFailure() {
    if (cfg.onLine ? !cfg.onLine() : typeof navigator !== "undefined" && navigator.onLine === false) {
      banner.set("offline");
      return;
    }
    if (await probe()) confirmed();
    else banner.set("connection");
  }

  const sleep = (ms: number) => (cfg.sleep ?? ((m) => new Promise<void>((r) => setTimeout(r, m))))(ms);

  return {
    banner,
    confirmed,
    networkFailure,
    retryDelay: () => sleep(RETRY_DELAY_MS),
    /** Clears a transient notice after a successful response. */
    ok() {
      banner.update((b) => (b === "offline" || b === "connection" ? "none" : b));
    },
    configure(d: SessionDeps) {
      Object.assign(cfg, d);
    },
  };
}

export type Session = ReturnType<typeof createSession>;
export const session = createSession();
