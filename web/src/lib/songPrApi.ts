// Typed wrappers over the Song PR operations (C253, C260). Scaffold by M5-F2; lane M5-W1 builds the panel and the patch page
// on them. Network only through the generated client.
import type { createApi } from "../api/client";
import type { components, operations } from "../api/schema";
import { failure, type Res } from "./crateApi";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];
export type SongPR = S["SongPR"];
export type SongPRSummary = S["SongPRSummary"];
export type PatchOp = S["PatchOp"];
export type PatchListQuery = NonNullable<operations["list_patches"]["parameters"]["query"]>;

/** The bounds the server enforces (operator decision 2026-11-03); the form shows them next to what a patch uses. */
export const PATCH_BOUNDS = { maxLines: 8, maxSections: 4, maxStyleTerms: 3, maxBPM: 20, maxAddressed: 10, maxOps: 20, maxRenders: 3 } as const;

export interface ProposeInput {
  baseTrackId: string;
  addressesCommentIds: string[];
  rationale: string;
  title?: string;
  extraSections?: number[];
  ops: PatchOp[];
}

/** propose_patch: the caller owns the idempotency key (one per user action, reused on retries). */
export async function proposePatch(api: Api, key: string, i: ProposeInput): Promise<Res<SongPR>> {
  const r = await api.POST("/api/patches", {
    params: { header: { "Idempotency-Key": key } },
    body: {
      base_track_id: i.baseTrackId,
      addresses_comment_ids: i.addressesCommentIds,
      rationale: i.rationale,
      ...(i.title ? { title: i.title } : {}),
      ...(i.extraSections?.length ? { extra_sections: i.extraSections } : {}),
      ops: i.ops,
    },
  });
  return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not propose the patch");
}

/** get_patch. */
export async function getPatch(api: Api, id: string): Promise<Res<SongPR>> {
  const r = await api.GET("/api/patches/{id}", { params: { path: { id } } });
  return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not load the patch");
}

/** list_patches, newest first. */
export async function listPatches(api: Api, query: PatchListQuery = {}): Promise<Res<S["PatchPage"]>> {
  const r = await api.GET("/api/patches", { params: { query } });
  return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not list the patches");
}

/** The render button of the patch page goes to the M3 confirm flow with this intent (C262). */
export function renderIntent(patchId: string, nSeeds: number): { mode: "song_pr"; patch_id: string; n_seeds: number } {
  return { mode: "song_pr", patch_id: patchId, n_seeds: nSeeds };
}
