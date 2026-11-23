// Helpers of the bulk-pods specs: the biggest intent the SPA can build is drafts times seeds (n_seeds is at most 10 per item), so a
// bulk batch of 30 takes is three drafts at ten seeds each.
import type { APIRequestContext, Page } from "@playwright/test";
import { apiJSON, uniq } from "./helpers";
import { recipe } from "./m3Api";
import { setIntent } from "./e2Api";

const MOTHERS = ["Syn song 02", "Syn song 03", "Syn song 04", "Syn song 05"];

/** Makes `drafts` backlog drafts (one per mother) and hands the Generate page an intent of drafts x `seeds` takes. */
export async function draftsIntent(page: Page, request: APIRequestContext, drafts: number, seeds: number): Promise<number> {
  const tracks = await apiJSON<{ items: { id: string; title: string }[] }>(request, "GET", "/api/tracks?limit=50");
  const collection = uniq("bulk");
  const ids: string[] = [];
  for (let i = 0; i < drafts; i++) {
    const mother = tracks.items.find((t) => t.title === MOTHERS[i]);
    if (!mother) throw new Error(`seed track ${MOTHERS[i]} missing`);
    const d = await apiJSON<{ id: string }>(request, "POST", "/api/drafts", { request: recipe(uniq("bulk"), 8100 + i), parent_track_id: mother.id, collection }, true);
    ids.push(d.id);
  }
  await setIntent(page, { mode: "from_draft", draft_ids: ids, n_seeds: seeds });
  return drafts * seeds;
}
