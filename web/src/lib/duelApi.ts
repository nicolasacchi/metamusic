// Thin adapters over the duel operations. Errors are thrown so callers (and runBlindRound) can retry.
import type { createApi } from "../api/client";
import type { components } from "../api/schema";
import { HttpError } from "./duelRoundRun";

type Api = ReturnType<typeof createApi>;
type Duel = components["schemas"]["Duel"];

export interface NewDuelOptions {
  blind: boolean;
  matchLoudness?: boolean;
  fromS?: number;
  toS?: number;
}

function fail(status: number, e: unknown, fallback: string): never {
  const m = typeof e === "object" && e !== null && "message" in e ? String((e as { message: unknown }).message) : "";
  throw new HttpError(status, m || fallback);
}

/** create_duel: the caller owns the idempotency key (one per user action, reused on retries). */
export async function createDuel(api: Api, a: string, b: string, key: string, o: NewDuelOptions): Promise<Duel> {
  const r = await api.POST("/api/duels", {
    params: { header: { "Idempotency-Key": key } },
    body: {
      a_track_id: a,
      b_track_id: b,
      blind: o.blind,
      ...(o.matchLoudness !== undefined ? { match_loudness: o.matchLoudness } : {}),
      ...(o.fromS !== undefined ? { from_s: o.fromS } : {}),
      ...(o.toS !== undefined ? { to_s: o.toS } : {}),
    },
  });
  if (!r.data) return fail(r.response.status, r.error, "Could not create the duel");
  return r.data;
}

export interface VerdictInput {
  verdict: "a" | "b" | "tie" | "both_bad";
  note: string;
  latency_ms: number;
  switches: number;
}

/** submit_duel: naturally idempotent, no key. Resolves to the revealed duel. */
export async function submitVerdict(api: Api, id: string, v: VerdictInput): Promise<Duel> {
  const r = await api.POST("/api/duels/{id}/verdicts", {
    params: { path: { id } },
    body: {
      verdict: v.verdict,
      ...(v.note ? { note: v.note } : {}),
      ...(v.latency_ms > 0 ? { latency_ms: v.latency_ms } : {}),
      ...(v.switches >= 0 ? { switches: v.switches } : {}),
    },
  });
  if (!r.data) return fail(r.response.status, r.error, "Could not submit the verdict");
  return r.data.duel;
}

/** get_duel: fresh signed clip URLs after a 403/404. */
export async function remintClips(api: Api, id: string): Promise<{ aUrl: string; bUrl: string } | null> {
  try {
    const r = await api.GET("/api/duels/{id}", { params: { path: { id } } });
    return r.data ? { aUrl: r.data.slots.a.clip.url, bUrl: r.data.slots.b.clip.url } : null;
  } catch {
    return null;
  }
}
