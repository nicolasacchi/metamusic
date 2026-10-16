// View-model helpers for the Song and Muse pages (C100.3, C100.4, C100.6). Pure; no network.
import type { components } from "../api/schema";
import type { Chip } from "./boardView";

type S = components["schemas"];

export const PENDING_REFETCH_MS = 1500;
export const MAX_PENDING_REFETCH = 3;

export interface TakeLike {
  id: string;
  recorded_at: string;
  is_pick: boolean;
  pinned: boolean;
  status: string;
}

function instant(s: string): number {
  const t = Date.parse(s);
  return Number.isNaN(t) ? 0 : t;
}

/** Pick first, then recorded_at descending (parsed instants: 20- and 24-character forms mix), then id descending. */
export function orderTakes<T extends TakeLike>(takes: readonly T[]): T[] {
  return [...takes].sort((a, b) => {
    if (a.is_pick !== b.is_pick) return a.is_pick ? -1 : 1;
    const d = instant(b.recorded_at) - instant(a.recorded_at);
    if (d !== 0) return d;
    return a.id < b.id ? 1 : a.id > b.id ? -1 : 0;
  });
}

/** Duel selection holds at most two take ids; picking a third drops the oldest, picking a selected one unselects it. */
export function selectForDuel(selected: readonly string[], id: string): string[] {
  if (selected.includes(id)) return selected.filter((x) => x !== id);
  return [...selected, id].slice(-2);
}

/** Delay before the next get_song refetch, or null when the page should stop (not pending, or three tries used). */
export function pendingDelay(pending: boolean, attempts: number): number | null {
  return pending && attempts < MAX_PENDING_REFETCH ? PENDING_REFETCH_MS : null;
}

/** A discarded take cannot be pinned; a pinned take can always be unpinned (D-L1-38 keeps ineligible pins). */
export function pinAction(t: Pick<TakeLike, "pinned" | "status">): "pin" | "unpin" | "none" {
  if (t.pinned) return "unpin";
  return t.status === "discarded" ? "none" : "pin";
}

interface ProposalLike {
  id: string;
  song_id: string;
  take_id: string;
}
export function acceptProposalRequest(p: ProposalLike) {
  return { op: "pin_pick" as const, songId: p.song_id, body: { take_id: p.take_id, proposal_id: p.id } };
}
export function dismissProposalRequest(p: Pick<ProposalLike, "id">) {
  return { op: "dismiss_pick_proposal" as const, proposalId: p.id };
}

interface TempoLike {
  requested_bpm?: number | null;
  measured_bpm?: number | null;
  mismatch?: boolean;
  octave?: boolean;
}
/** "requested 76 · measured 75 ✓" (mismatch ⚠, octave ≈, requested-only "estimate"); empty when nothing is known. */
export function tempoText(t: TempoLike | null | undefined): string {
  if (!t) return "";
  const r = t.requested_bpm ?? null;
  const m = t.measured_bpm ?? null;
  if (r !== null && m !== null) return `requested ${r} · measured ${m} ${t.octave ? "≈" : t.mismatch ? "⚠" : "✓"}`;
  if (r !== null) return `requested ${r} · estimate`;
  if (m !== null) return `measured ${m}`;
  return "";
}

/** One-line plain-words rendering of a Pick event. */
export function eventText(e: { reason: string; to_source: string }): string {
  switch (e.reason) {
    case "pin":
      return "pinned";
    case "unpin":
      return `unpinned, now by ${e.to_source}`;
    case "pin_ineligible":
      return `pin not eligible, now ${e.to_source}`;
    case "pin_restored":
      return "pin applies again";
    case "initial":
      return `first Pick (${e.to_source})`;
    case "emptied":
      return "no take left";
    case "ladder":
      return "by duels";
    case "stars":
      return "by stars";
    case "newest":
      return "newest take";
    default:
      return e.reason;
  }
}

/** A Muse page row as a Board chip (the Muse page reuses SongChip). */
export function summaryToChip(s: S["SongSummary"]): Chip {
  return {
    id: s.id,
    title: s.title,
    state: s.state,
    takes: s.takes,
    pick_source: s.pick?.source ?? null,
    pick_take_id: s.pick?.take_id ?? null,
    queued: s.queued_jobs > 0,
    draft_id: s.draft_id,
    tempo_mismatch: false,
  };
}
