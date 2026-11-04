// Typed wrapper over suggest_duel_pairs (C258) for the "Smart round". Scaffold by M5-F2; lane M5-W2 builds on it.
import type { createApi } from "../api/client";
import type { components, operations } from "../api/schema";
import { failure, type Res } from "./crateApi";

type Api = ReturnType<typeof createApi>;
export type PairSuggestions = components["schemas"]["PairSuggestions"];
export type PairSuggestion = components["schemas"]["PairSuggestion"];
export type SuggestQuery = NonNullable<operations["suggest_duel_pairs"]["parameters"]["query"]>;

/** suggest_duel_pairs: the pairs whose next human verdict teaches the ladder most. */
export async function suggestPairs(api: Api, query: SuggestQuery = {}): Promise<Res<PairSuggestions>> {
  const r = await api.GET("/api/duels/suggestions", { params: { query } });
  return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not suggest pairs");
}
