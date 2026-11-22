// Typed wrappers over the diction operations and the Recover flow (M5b C345, C347.3-.5), plus the small words the Diction Lab page
// shows. Network only through the generated client.
import type { createApi } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { components } from "../api/schema";
import { failure, type Res } from "./crateApi";
import { normalizePage, type MarkIn } from "./karaoke";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];
export type LexiconEntry = S["LexiconEntry"];
export type LexiconPage = S["LexiconPage"];
export type Calibration = S["Calibration"];
export type RecoverResult = S["RecoverResult"];

/** The lists of an entry are lists even when the server sent null (a Go nil slice). */
export function entryOf(e: LexiconEntry): LexiconEntry {
  return { ...e, typical_heard: e.typical_heard ?? [], examples: e.examples ?? [] };
}

async function guard<T>(fallback: string, run: () => Promise<Res<T>>): Promise<Res<T>> {
  try {
    return await run();
  } catch {
    return { ok: false, status: 0, code: "NETWORK", message: fallback + ": network error", stale: false };
  }
}

/** get_track_diction: one page of lines (pass the previous page's next_cursor for the next). */
export function getTrackDiction(api: Api, id: string, cursor?: string | null): Promise<Res<S["DictionLines"]>> {
  return guard("Could not load the diction of this take", async () => {
    const r = await api.GET("/api/tracks/{id}/diction", { params: { path: { id }, query: cursor ? { cursor } : {} } });
    return r.data ? { ok: true, value: normalizePage(r.data) } : failure(r.response.status, r.error, "Could not load the diction of this take");
  });
}

/** set_diction_marks: an atomic batch; a person's mark overrides the recogniser's class on a line. */
export function setDictionMarks(api: Api, id: string, marks: MarkIn[]): Promise<Res<S["TrackMarks"]>> {
  return guard("Could not save the mark", async () => {
    const r = await api.PUT("/api/tracks/{id}/diction/marks", { params: { path: { id } }, body: { marks } });
    return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not save the mark");
  });
}

export interface LexiconFilters {
  /** "" is every language. */
  lang: string;
  risk: "all" | "risky" | "other";
  q: string;
}

/** The query of get_diction_lexicon for the page's filters; empty filters are left out. */
export function lexiconQuery(f: LexiconFilters): { lang?: string; risk?: boolean; q?: string } {
  const q = f.q.trim();
  return { ...(f.lang ? { lang: f.lang } : {}), ...(f.risk === "all" ? {} : { risk: f.risk === "risky" }), ...(q ? { q } : {}) };
}

/** get_diction_lexicon: risky words first. */
export function getLexicon(api: Api, o: { lang?: string; risk?: boolean; q?: string; limit?: number; cursor?: string | null }): Promise<Res<LexiconPage>> {
  return guard("Could not load the lexicon", async () => {
    const query = { ...(o.lang ? { lang: o.lang } : {}), ...(o.risk === undefined ? {} : { risk: o.risk }), ...(o.q ? { q: o.q } : {}), ...(o.limit ? { limit: o.limit } : {}), ...(o.cursor ? { cursor: o.cursor } : {}) };
    const r = await api.GET("/api/diction/lexicon", { params: { query } });
    return r.data ? { ok: true, value: { ...r.data, entries: (r.data.entries ?? []).map(entryOf) } } : failure(r.response.status, r.error, "Could not load the lexicon");
  });
}

/** update_lexicon_entry: ignore or restore a word, or set its note (an empty note removes it). */
export function updateLexiconEntry(api: Api, lang: string, word: string, patch: { status?: "active" | "ignored"; note?: string }): Promise<Res<LexiconEntry>> {
  return guard("Could not update the word", async () => {
    const r = await api.PATCH("/api/diction/lexicon/{lang}/{word}", { params: { path: { lang, word } }, body: patch });
    return r.data ? { ok: true, value: entryOf(r.data) } : failure(r.response.status, r.error, "Could not update the word");
  });
}

/**
 * recover_recipe for one click: the returned function may be called again to retry and sends the same Idempotency-Key. The lyrics are melody-first (the default). It creates a
 * draft and spends nothing; rendering the draft goes through the ordinary estimate and confirmation.
 */
export function recoverRecipe(api: Api, id: string, o: { style?: string }): () => Promise<Res<RecoverResult>> {
  const style = (o.style ?? "").trim();
  return idempotentAction((key) =>
    guard("Could not recover the recipe", async () => {
      const r = await api.POST("/api/tracks/{id}/recover", { params: { path: { id }, header: { "Idempotency-Key": key } }, body: { cot: "melody", ...(style ? { style } : {}) } });
      return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not recover the recipe");
    }),
  );
}

/** "4 of 6 mangled (67 %)". */
export function rateText(e: { attempts: number; mangled: number }): string {
  if (e.attempts <= 0) return "never heard";
  return `${e.mangled} of ${e.attempts} mangled (${Math.round((e.mangled / e.attempts) * 100)} %)`;
}

/** "pane (2), pene (1)". */
export function heardText(h: readonly { text: string; n: number }[]): string {
  return h.map((x) => `${x.text} (${x.n})`).join(", ");
}

export const CALIBRATION_LINES = 60;
export const CALIBRATION_TRACKS = 6;

export function calibrationText(c: Calibration): string {
  const target = `${CALIBRATION_LINES} lines over ${CALIBRATION_TRACKS} takes`;
  if (c.lines_marked === 0) return `No lines marked yet. About ${target} calibrate the thresholds.`;
  const where = c.lang === "all" ? "" : ` in ${c.lang}`;
  return `${c.lines_marked} lines marked on ${c.tracks_marked} takes (${c.clear} clear, ${c.unclear} unclear)${where}. Target: ${target}.`;
}

export type RecoverStage = "wait" | "analyze" | "pending" | "retry" | "ready" | "unavailable";

/** The check state when asking for it failed on the network (not a stored check that errored): the panel offers a retry. */
export const UNREACHABLE = "unreachable";

/**
 * Where the Recover flow of a reference take stands. `dossier` is the Ears dossier (null while it loads), `checkState` the state of
 * get_track_tier_b when the dossier says a check is stored (null while it loads, "" when it was not asked): no check ⇒ analyze;
 * an ok check ⇒ ready; a check still running ⇒ pending (progress, no button); a failed read ⇒ retry; a stored check with no transcript (error, skipped) ⇒ unavailable, which is terminal for that take.
 */
export function recoverStage(dossier: { tier: string; flags: readonly { code: string }[] } | null, checkState: string | null): RecoverStage {
  if (!dossier) return "wait";
  const stored = dossier.tier === "B" || dossier.flags.some((f) => f.code === "listening_check_failed");
  if (!stored) return "analyze";
  if (checkState === null) return "wait";
  if (checkState === "ok") return "ready";
  if (checkState === "pending") return "pending";
  if (checkState === UNREACHABLE) return "retry";
  if (checkState === "none" || checkState === "missing") return "analyze";
  return "unavailable";
}
