// The one `me` store (U1 C313.1). Loaded once before the first render (bootMe), refreshed when a step-up completes
// (the deadline moves), and read everywhere through `can(opId)`. It replaces the independent /api/me callers.
import { api } from "../api/client";
import { dropMediaCache, dropOfflineDb } from "../api/browserState";
import { applyUserState, canOp, fetchWhoami, readLast, roleOf, writeLast, type KV, type Role, type Whoami } from "./me";

export const me = $state<{
  loaded: boolean;
  kind: Whoami["kind"] | null;
  role: Role | null;
  user: string | null;
  name: string;
  via: string;
  scopes: string[];
  allowed: string[];
  stepUpUntil: string | null;
  /** The numbers come from the last stored whoami (offline start), not from the server. */
  stale: boolean;
}>({ loaded: false, kind: null, role: null, user: null, name: "", via: "", scopes: [], allowed: [], stepUpUntil: null, stale: false });

/** May the caller invoke this operation? False until the store has loaded. */
export function can(op: string): boolean {
  return canOp(me.allowed, op);
}

/** A snapshot for code that wants a plain object (view-model helpers, unit tests). */
export function viewer() {
  return me.loaded && me.kind ? { kind: me.kind, scopes: me.scopes, allowed: me.allowed } : null;
}

function apply(w: Whoami, stale: boolean) {
  me.kind = w.kind;
  me.role = roleOf(w.role);
  me.user = w.user ?? null;
  me.name = w.name;
  me.via = w.via;
  me.scopes = w.scopes;
  me.allowed = w.allowed_ops;
  me.stepUpUntil = w.step_up_until;
  me.stale = stale;
  me.loaded = true;
}

function storage(): KV | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

let booting: Promise<boolean> | null = null;

/**
 * Load whoami and reconcile browser state with the user; resolves false when there is no answer and no stored one
 * (the shell then offers a retry). Run before App is imported: modules read browser state when they load.
 */
export function bootMe(): Promise<boolean> {
  booting ??= (async () => {
    const kv = storage();
    let w = await fetchWhoami({ GET: (p, i) => api.GET(p, i) });
    let stale = false;
    if (!w && kv) {
      w = readLast(kv);
      stale = w !== null;
    }
    if (!w) return false;
    if (kv && !stale) {
      await applyUserState({ kv, dropMediaCache, dropOfflineDb }, w.user, roleOf(w.role));
      writeLast(kv, w);
    }
    apply(w, stale);
    return true;
  })().finally(() => {
    booting = null;
  });
  return booting;
}

/** Re-read whoami (step-up done): the user never changes inside a session, so no reset here. */
export async function refreshMe(): Promise<void> {
  const w = await fetchWhoami({ GET: (p, i) => api.GET(p, i) });
  if (!w) return;
  const kv = storage();
  if (kv) writeLast(kv, w);
  apply(w, false);
}

if (typeof window !== "undefined") {
  window.addEventListener("mm:stepup-done", () => {
    if (me.loaded) void refreshMe();
  });
}
