// Smart round (C258, C262): suggest_duel_pairs -> create_duel, with an optional section lock. Pure helpers plus two thin
// api calls. Blindness: a pair is shown by its information score only while the round is blind, and nothing here reads a
// duel's slots or windows (those stay hidden by the server until the verdict); a created duel is opened, never described.
import type { createApi } from "../api/client";
import { failure } from "./crateApi";
import { suggestPairs, type PairSuggestion, type SuggestQuery } from "./pairSuggestApi";

type Api = ReturnType<typeof createApi>;

export interface SectionChoice {
  /** "" for no section lock (the usual 30% window). */
  kind: string;
  /** 1-based; ignored without a kind. */
  ordinal: number;
}

export const NO_SECTION: SectionChoice = { kind: "", ordinal: 1 };

/** The kinds the picker offers; the server answers SECTION_UNTIMED for a track without a timed one. */
export const SECTION_KINDS = ["intro", "verse", "pre-chorus", "chorus", "bridge", "outro"] as const;

/** Where the suggestions come from: a lineage root, or the recent rated tracks. */
export interface SmartScope {
  scope: "lineage" | "global";
  rootTrackId: string;
  k: number;
  section: SectionChoice;
}

export function suggestQuery(s: SmartScope): SuggestQuery {
  const q: SuggestQuery = { scope: s.scope, k: Math.min(15, Math.max(1, Math.trunc(s.k) || 6)) };
  if (s.scope === "lineage" && s.rootTrackId.trim()) q.root_track_id = s.rootTrackId.trim();
  if (s.section.kind) {
    q.section_kind = s.section.kind;
    q.section_ordinal = Math.max(1, Math.trunc(s.section.ordinal) || 1);
  }
  return q;
}

/** Lineage needs a root; everything else can ask. */
export const canSuggest = (s: SmartScope): boolean => s.scope === "global" || s.rootTrackId.trim() !== "";

/** The body's `section` selector, or undefined for no lock. */
export function sectionSelector(c: SectionChoice): { kind: string; ordinal: number } | undefined {
  return c.kind ? { kind: c.kind, ordinal: Math.max(1, Math.trunc(c.ordinal) || 1) } : undefined;
}

/** A pair's row title: titles only when the round is not blind, "Pair n" otherwise. */
export function pairTitle(p: Pick<PairSuggestion, "a_title" | "b_title">, i: number, blind: boolean): string {
  return blind ? `Pair ${i + 1}` : `${p.a_title} vs ${p.b_title}`;
}

/** "score 0.123 · 2 human verdicts": the expected information of one more verdict, and how many humans already judged the pair. */
export function pairHint(p: Pick<PairSuggestion, "score" | "human_verdicts">): string {
  const v = p.human_verdicts === 1 ? "1 human verdict" : `${p.human_verdicts} human verdicts`;
  return `score ${p.score.toFixed(3)} · ${v}`;
}

const REASON_TEXT: Record<string, string> = {
  SECTION_UNTIMED: "One of the tracks has no timed sections yet, so a section cannot be locked.",
  SECTION_NOT_FOUND: "One of the tracks has no such section.",
  CLIP_WINDOW_INVALID: "The two section windows could not be made equal.",
};

export type SuggestOutcome = { kind: "pairs"; pairs: PairSuggestion[]; verdictsCounted: number } | { kind: "unavailable" } | { kind: "error"; message: string };

/** suggest_duel_pairs, with 501/503 mapped to "unavailable". */
export async function loadSuggestions(api: Api, s: SmartScope): Promise<SuggestOutcome> {
  try {
    const r = await suggestPairs(api, suggestQuery(s));
    if (r.ok) return { kind: "pairs", pairs: r.value.pairs, verdictsCounted: r.value.model.verdicts_counted };
    if (r.status === 501 || r.status === 503) return { kind: "unavailable" };
    return { kind: "error", message: REASON_TEXT[r.code] ?? r.message };
  } catch {
    return { kind: "error", message: "Could not suggest pairs: network error" };
  }
}

export type StartOutcome = { kind: "created"; id: string } | { kind: "error"; message: string; status: number };

/** create_duel for one suggested pair; blind by default. The caller owns the key (one per pair and choice). */
export async function startPair(api: Api, p: Pick<PairSuggestion, "a_track_id" | "b_track_id">, key: string, blind: boolean, section: SectionChoice): Promise<StartOutcome> {
  try {
    const sel = sectionSelector(section);
    const r = await api.POST("/api/duels", {
      params: { header: { "Idempotency-Key": key } },
      body: { a_track_id: p.a_track_id, b_track_id: p.b_track_id, blind, ...(sel ? { section: sel } : {}) },
    });
    if (r.data) return { kind: "created", id: r.data.id };
    const f = failure(r.response.status, r.error, "Could not create the duel");
    return { kind: "error", status: f.status, message: f.status === 501 || f.status === 503 ? "Section duels are not available on this server yet." : (REASON_TEXT[f.code] ?? f.message) };
  } catch {
    return { kind: "error", status: 0, message: "Could not create the duel: network error" };
  }
}

