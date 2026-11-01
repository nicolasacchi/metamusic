import { readFileSync } from "node:fs";
import { resolve } from "node:path";

type Doc = { paths: Record<string, Record<string, { operationId?: string; "x-mm"?: { ui?: string } }>> };

/**
 * UI operations whose pages are not built yet: the six L3 operations with a UI route (the web lanes WM2, WS, WA1 and WA2 build
 * their pages and INT-W removes each id with its spec; src/lib/e2e-coverage.test.ts fails on an id that is no longer a UI
 * operation).
 */
export const PENDING_UI: ReadonlySet<string> = new Set([
  "decide_taxonomy_proposal",
  "get_adherence",
  "get_song_atlas",
  "list_next_actions",
  "list_taxonomy_proposals",
  "suggest_sequence",
]);

/** Operation ids whose x-mm.ui is a route (not "none:<reason>"): the SPA must exercise them, except the PENDING_UI ones. */
export function uiOperations(openapiPath = resolve(import.meta.dirname, "../../openapi.json")): string[] {
  const doc = JSON.parse(readFileSync(openapiPath, "utf8")) as Doc;
  const ids: string[] = [];
  for (const item of Object.values(doc.paths)) {
    for (const op of Object.values(item)) {
      const ui = op["x-mm"]?.ui;
      if (op.operationId && ui && !ui.startsWith("none:") && !PENDING_UI.has(op.operationId)) ids.push(op.operationId);
    }
  }
  return ids.sort();
}

export function missingOperations(expected: string[], served: string[]): string[] {
  const seen = new Set(served);
  return expected.filter((id) => !seen.has(id));
}

/** Union of the served-operation lists written by the shards of a sharded run (MM_E2E_SERVED_OUT). */
export function servedUnion(files: string[]): string[] {
  const all = new Set<string>();
  for (const f of files) for (const id of JSON.parse(readFileSync(f, "utf8")) as string[]) all.add(id);
  return [...all].sort();
}
