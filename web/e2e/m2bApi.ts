// API helpers for the M2b specs: duels are created through the same REST operations the SPA uses, as
// the e2e human principal, with an Idempotency-Key (apiJSON(..., create = true)).
import type { APIRequestContext } from "@playwright/test";
import { apiJSON, trackByTitle, type Item } from "./helpers";

export interface DuelSlot {
  clip: { url: string; content_type: string };
  track: { id: string; title: string; seed: number } | null;
}
export interface DuelDTO {
  id: string;
  blind: boolean;
  state: "open" | "decided" | "expired";
  slots: { a: DuelSlot; b: DuelSlot };
  human_verdict?: { verdict: string; winner_track_id: string | null } | null;
}
export interface LadderDTO {
  items: { rank: number; track_id: string; title: string }[];
  counts: Record<string, number>;
}

/** The seeded sibling pair: the root (seed 1001) and its inferred child (seed 2001). */
export async function siblings(request: APIRequestContext): Promise<{ root: Item; child: Item }> {
  return { root: await trackByTitle(request, "Syn song 01 s1001"), child: await trackByTitle(request, "Syn song 01 s2001") };
}

export function createDuel(request: APIRequestContext, a: string, b: string, blind = true): Promise<DuelDTO> {
  return apiJSON<DuelDTO>(request, "POST", "/api/duels", { a_track_id: a, b_track_id: b, blind, match_loudness: true }, true);
}

export function getDuel(request: APIRequestContext, id: string): Promise<DuelDTO> {
  return apiJSON<DuelDTO>(request, "GET", `/api/duels/${id}`);
}

export async function decide(request: APIRequestContext, id: string, verdict: "a" | "b" | "tie" | "both_bad"): Promise<DuelDTO> {
  return (await apiJSON<{ duel: DuelDTO }>(request, "POST", `/api/duels/${id}/verdicts`, { verdict })).duel;
}

export function ladder(request: APIRequestContext, root: string): Promise<LadderDTO> {
  return apiJSON<LadderDTO>(request, "GET", `/api/ladder?scope=lineage&root_track_id=${root}&limit=100`);
}
