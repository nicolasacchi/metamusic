import { readFileSync } from "node:fs";
import { resolve } from "node:path";

type Doc = { paths: Record<string, Record<string, { operationId?: string; "x-mm"?: { ui?: string } }>> };

/**
 * UI operations whose pages are not built yet. The M5 operations left in M5 INT-W (the m5-* specs exercise them all);
 * what remains is E2's /engines matrix. src/lib/e2e-coverage.test.ts fails on an id that is no longer a UI operation, so an
 * entry is only ever added for a page that is still to come.
 */
export const PENDING_UI: ReadonlySet<string> = new Set<string>([
  // DESK-W2 (lane desk-web) adds the audition player and the Audition button on /desk/plans/:id (m5b-desk C363); it removes these two.
  "create_plan_audition",
  "get_plan_audition",
  // M6N-W1 (lane loop-web-a) adds the /mandates and /mandates/:id pages and the secure /human/mandates/new form (m6-night C388.1); it removes these four.
  "create_mandate",
  "list_mandates",
  "get_mandate",
  "revoke_mandate",
  // M6N-W1 (lane loop-web-a) adds the /critics page (m6n-night C388.1, spec m6n-critics.spec.ts); it removes this one.
  "get_critic_bench",
  // M6N-W2 (lane loop-web-b) adds the /morning and /morning/:id pages: the player's crate mode, blind rating, the reveal and the settings
  // (m6-night C384, spec m6n-morning.spec.ts); it removes these seven.
  "get_morning_crate_settings",
  "update_morning_crate_settings",
  "list_morning_crates",
  "get_morning_crate",
  "build_morning_crate",
  "submit_morning_crate_events",
  "reveal_morning_crate",
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
