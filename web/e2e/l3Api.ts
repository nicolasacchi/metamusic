// API helpers for the L3 specs (Atlas, Proposals, What next, Sequence, entry points). Like the L1 and L2 specs, every spec
// makes its own Songs under a per-run unique title and does not depend on a total another spec changes. Nothing here deletes.
import type { APIRequestContext } from "@playwright/test";
import { apiJSON } from "./helpers";
import { composeSong, getSong } from "./l1Api";

export interface AtlasPointOut {
  id: string;
  t: string;
  x: number;
  y: number;
  st: number;
  xb?: string;
  yb?: string;
}
export interface AtlasOut {
  points: AtlasPointOut[];
  totals: { scope: number; plotted: number };
  next_cursor: string | null;
  pending: boolean;
}
export interface FeaturesOut {
  tempo: { bpm: number | null; basis: string | null };
  energy: { value: number | null; basis: string | null };
  key: { camelot: string | null; basis: string | null };
  pending: boolean;
}
export interface NextActionOut {
  code: string;
  audience: string;
  subject: { kind: string; id: string | null };
  hint?: { op: string; args?: Record<string, unknown> } | null;
  text: string;
}
export interface ProposalOut {
  id: string;
  state: "open" | "accepted" | "rejected" | "expired";
  mappings: { term: string; facet: string; value: string; op: string }[];
  accepted: number[] | null;
}
export interface SequenceOut {
  explanation: { id: string; strategy: string; stale: boolean };
}

export const getAtlas = (request: APIRequestContext, query: string) => apiJSON<AtlasOut>(request, "GET", `/api/atlas?${query}`);

/** The Atlas point of a Song (a one-Song scope by its title), or undefined while it is not on the map. */
export async function atlasPointOf(request: APIRequestContext, title: string): Promise<AtlasPointOut | undefined> {
  const a = await getAtlas(request, `q=${encodeURIComponent(title)}&limit=50`);
  return a.points.find((p) => p.t === title);
}

/** Polls until the Song's features row is settled (`pending` false) and returns it. */
export async function waitFeatures(request: APIRequestContext, songId: string, timeoutMs = 60_000): Promise<FeaturesOut> {
  const deadline = Date.now() + timeoutMs;
  let last: FeaturesOut | null | undefined;
  while (Date.now() < deadline) {
    const d = await apiJSON<{ features?: FeaturesOut | null }>(request, "GET", `/api/songs/${songId}`);
    last = d.features;
    if (last && !last.pending) return last;
    await new Promise((r) => setTimeout(r, 300));
  }
  throw new Error(`features of ${songId} still pending (${JSON.stringify(last)})`);
}

export const nextActions = async (request: APIRequestContext, query = "limit=50") => (await apiJSON<{ items: NextActionOut[]; total: number }>(request, "GET", `/api/next-actions?${query}`)).items;

/** Proposes a taxonomy mapping as the agent (propose_taxonomy is agent-face: a human is refused with HUMAN_ONLY). */
export async function proposeTaxonomy(agent: APIRequestContext, terms: { term: string; facet: string; value: string }[]): Promise<ProposalOut> {
  return apiJSON<ProposalOut>(agent, "POST", "/api/taxonomy-proposals", { rationale: "e2e: l3 proposal", mappings: terms.map((m) => ({ ...m, op: "add" })) }, true);
}

export const getProposal = async (request: APIRequestContext, id: string) => (await apiJSON<{ items: ProposalOut[] }>(request, "GET", `/api/taxonomy-proposals?limit=100&state=open&state=accepted&state=rejected`)).items.find((p) => p.id === id);

export interface TitledAt {
  songId: string;
  root: string;
  title: string;
  bpm: number;
}

/** Rendered Songs of one take each, one per tempo (`<bpm> bpm` in the style), titled `<tag> <i>`: pass a unique tag, then `q=<tag>` finds exactly them. */
export async function songsAt(request: APIRequestContext, tag: string, bpms: number[]): Promise<TitledAt[]> {
  const out: TitledAt[] = [];
  for (const [i, bpm] of bpms.entries()) {
    const s = await composeSong(request, tag, 1, `english, synth pop, ${bpm} bpm`);
    const title = `${tag} ${i}`;
    const rev = (await getSong(request, s.songId)).song.revision;
    await apiJSON(request, "PATCH", `/api/songs/${s.songId}`, { revision: rev, title });
    out.push({ songId: s.songId, root: s.root, title, bpm });
  }
  return out;
}
