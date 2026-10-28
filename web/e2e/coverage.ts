import { readFileSync } from "node:fs";
import { resolve } from "node:path";

type Doc = { paths: Record<string, Record<string, { operationId?: string; "x-mm"?: { ui?: string } }>> };

/**
 * UI operations whose pages are not built yet: the 17 L2 operations (Records, tapes, Crates, curation settings) have their
 * routes frozen by lane F2 and their pages built by lanes W1 to W3. Lane INT-W empties this set in the same change that adds
 * the specs serving them (src/lib/e2e-coverage.test.ts fails on an id that is no longer a UI operation).
 */
export const PENDING_UI: ReadonlySet<string> = new Set([
  "list_records", "get_record", "create_record", "update_record", "edit_record_slots", "get_record_changes", "propose_record", "publish_record",
  "export_record_tape", "get_tape", "list_crates", "get_crate", "create_crate", "update_crate", "edit_crate_items", "get_curation_settings",
  "set_curation_settings",
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
