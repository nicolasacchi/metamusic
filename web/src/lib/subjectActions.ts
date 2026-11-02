// The next actions of one Song or Muse (`list_next_actions` with a subject). Failures read as "none".
import { api } from "../api/client";
import type { NextActionItem } from "./featuresView";

export async function fetchSubjectActions(kind: "song" | "muse", id: string, limit = 20): Promise<NextActionItem[]> {
  try {
    const r = await api.GET("/api/next-actions", { params: { query: { subject_kind: kind, subject_id: id, limit } } });
    return r.data?.items ?? [];
  } catch {
    return [];
  }
}
