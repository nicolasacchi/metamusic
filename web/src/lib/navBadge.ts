// Nav badge for "Duels": list_duels(state=open, limit=1).total. Never next_duel, which renders clips.
import type { components } from "../api/schema";

export interface BadgeClient {
  GET(
    path: "/api/duels",
    init: { params: { query: { state: ("open" | "decided" | "expired")[]; limit: number } } },
  ): Promise<{ data?: Pick<components["schemas"]["DuelPage"], "total"> & Partial<components["schemas"]["DuelPage"]>; error?: unknown }>;
}

export async function loadDuelBadge(client: BadgeClient): Promise<number | null> {
  try {
    const r = await client.GET("/api/duels", { params: { query: { state: ["open"], limit: 1 } } });
    return r.data ? r.data.total : null;
  } catch {
    return null;
  }
}
