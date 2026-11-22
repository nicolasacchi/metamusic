import { readFileSync } from "node:fs";
import { resolve } from "node:path";

type Doc = { paths: Record<string, Record<string, { operationId?: string; "x-mm"?: { ui?: string } }>> };

/**
 * UI operations whose pages are not built yet. The M5 operations left in M5 INT-W (the m5-* specs exercise them all);
 * what remains is E2's /engines matrix. src/lib/e2e-coverage.test.ts fails on an id that is no longer a UI operation, so an
 * entry is only ever added for a page that is still to come.
 */
export const PENDING_UI: ReadonlySet<string> = new Set<string>([
  // EARS-W2 (lane ears-web-diction) adds the karaoke view on /tracks/:id and the /diction page (m5b-ears C345); it removes these four.
  "get_track_diction",
  "set_diction_marks",
  "get_diction_lexicon",
  "update_lexicon_entry",
  // DESK-W1 (lane desk-web) adds the Desk pages /desk and /desk/plans/:id and the "Open in the Desk" action on /tracks/:id
  // (m5b-desk C363); it removes these six.
  "list_plans",
  "get_plan",
  "check_abc",
  "create_plan_from_take",
  "edit_plan",
  "set_plan_verdict",
  // M6N-W1 (lane loop-web-a) adds the /mandates and /mandates/:id pages and the secure /human/mandates/new form (m6-night C388.1); it removes these four.
  "create_mandate",
  "list_mandates",
  "get_mandate",
  "revoke_mandate",
  // EARS-W2 also adds the "Create draft from transcript" button of the reference panel on /tracks/:id (m5b-ears C343.2, C347.5); it removes this one.
  "recover_recipe",
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
