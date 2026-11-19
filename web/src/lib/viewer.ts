import type { Viewer } from "./genIntent";
import { viewer } from "./me.svelte";

/** The calling principal (identity, scopes and allowed ops) from the `me` store; null when it never loaded. No request. */
export function fetchViewer(): Promise<Viewer | null> {
  return Promise.resolve(viewer());
}
