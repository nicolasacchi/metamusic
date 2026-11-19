// The caller, as the web app knows it (U1 C313.1): a pure model over GET /api/me. The reactive store is me.svelte.ts.
// `can(op)` answers from whoami.allowed_ops, which the server computes with the same gate that refuses the call, so a
// control is shown exactly when the server would let the click through. Hiding is cosmetic; the server stays the authority.
import type { components } from "../api/schema";

export type Whoami = components["schemas"]["WhoamiOutBody"];
export type Role = "owner" | "editor" | "listener";

export function roleOf(r: string | null | undefined): Role | null {
  return r === "owner" || r === "editor" || r === "listener" ? r : null;
}

/** An op id is allowed when the server listed it. Unknown or not-yet-loaded callers can do nothing. */
export function canOp(allowed: readonly string[] | null | undefined, op: string): boolean {
  return !!allowed && allowed.includes(op);
}

/** Per-user browser state (C313.7): the last username, and the last whoami for an offline start. */
export const USER_KEY = "mm:me:user";
export const LAST_KEY = "mm:me:last";
export const MEDIA_CACHE = "mm-media-v1";
export const OFFLINE_DB = "mm-offline";

/** The slice of Storage this module needs. */
export interface KV {
  readonly length: number;
  key(i: number): string | null;
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
  removeItem(k: string): void;
}

export interface ResetEnv {
  kv: KV;
  dropMediaCache(): Promise<void>;
  dropOfflineDb(): Promise<void>;
}

function mmKeys(kv: KV): string[] {
  const out: string[] = [];
  for (let i = 0; i < kv.length; i++) {
    const k = kv.key(i);
    if (k && k.startsWith("mm:")) out.push(k);
  }
  return out;
}

/**
 * Must the previous user's browser state go? Yes when a different user is signed in now, or when no user was ever
 * recorded, some `mm:*` state exists and the caller is not the Owner. The Owner adopts state with no recorded user:
 * before U1 the Owner was the only person who could sign in, so those keys (and the offline downloads) are theirs.
 */
export function mustReset(stored: string | null, user: string, role: Role | null, anyMm: boolean): boolean {
  if (stored !== null) return stored !== user;
  return anyMm && role !== "owner";
}

/**
 * Called once at boot with the signed-in user, before anything reads browser state: on a user change it clears every
 * `mm:*` localStorage key, the `mm-media-v1` cache and the `mm-offline` database, then records the user.
 * Returns whether anything was cleared. A failure to clear the caches never blocks boot.
 */
export async function applyUserState(env: ResetEnv, user: string | null, role: Role | null): Promise<boolean> {
  if (!user) return false;
  const stored = env.kv.getItem(USER_KEY);
  const keys = mmKeys(env.kv);
  const reset = mustReset(stored, user, role, keys.some((k) => k !== USER_KEY && k !== LAST_KEY));
  if (reset) {
    for (const k of keys) env.kv.removeItem(k);
    await Promise.allSettled([env.dropMediaCache(), env.dropOfflineDb()]);
  }
  if (stored !== user) env.kv.setItem(USER_KEY, user);
  return reset;
}

/** The last whoami, kept for an offline start (no network, same browser). Anything malformed is ignored. */
export function readLast(kv: Pick<KV, "getItem">): Whoami | null {
  try {
    const raw = kv.getItem(LAST_KEY);
    if (!raw) return null;
    const v: unknown = JSON.parse(raw);
    if (typeof v !== "object" || v === null) return null;
    const w = v as Partial<Whoami>;
    if (!Array.isArray(w.allowed_ops) || !Array.isArray(w.scopes) || typeof w.kind !== "string" || typeof w.name !== "string") return null;
    return v as Whoami;
  } catch {
    return null;
  }
}

export function writeLast(kv: Pick<KV, "setItem">, w: Whoami): void {
  try {
    kv.setItem(LAST_KEY, JSON.stringify(w));
  } catch {
    /* storage full or blocked: the next online start asks again */
  }
}

export interface MeClient {
  GET(path: "/api/me", init: Record<string, never>): Promise<{ data?: Whoami }>;
}

/** One whoami call; null when it failed (network, session lost) so the caller can fall back. */
export async function fetchWhoami(client: MeClient): Promise<Whoami | null> {
  try {
    const r = await client.GET("/api/me", {});
    return r.data ?? null;
  } catch {
    return null;
  }
}
