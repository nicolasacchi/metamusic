import { api } from "../api/client";
import type { Viewer } from "./genIntent";

/** The calling principal (identity and scopes) for gating generation buttons; null when unknown. */
export async function fetchViewer(): Promise<Viewer | null> {
  try {
    const r = await api.GET("/api/me");
    return r.data ? { kind: r.data.kind, scopes: r.data.scopes } : null;
  } catch {
    return null;
  }
}
