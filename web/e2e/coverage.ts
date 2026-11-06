import { readFileSync } from "node:fs";
import { resolve } from "node:path";

type Doc = { paths: Record<string, Record<string, { operationId?: string; "x-mm"?: { ui?: string } }>> };

/**
 * UI operations whose pages are not built yet: the M5 operations frozen by M5-F2, one line
 * per lane (M5-W1, M5-W2, M5-V1 delete their ids when their specs exercise them). src/lib/e2e-coverage.test.ts fails on an id
 * that is no longer a UI operation, so an entry is only ever added for a page that is still to come.
 */
export const PENDING_UI: ReadonlySet<string> = new Set<string>([
  "propose_patch", "get_patch", "list_patches", // M5-W1: Song PR panel and patch page
  "get_listen_heat", // M5-W1: heat strip
  "list_callbacks", "get_callback", "submit_callback_verdict", // M5-W2: Judge Callbacks
  "suggest_duel_pairs", // M5-W2: Smart round
  "create_voice_note", // M5-V1/W2: voice recorder
  "list_engines", // E2-W1b: the /engines capability matrix (E2-W-INT adds the spec)
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
