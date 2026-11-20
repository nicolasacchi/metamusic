// API helpers for the multi-user specs (U1 C314.7): one request context per configured user on the same worker server.
import type { APIRequestContext, PlaywrightWorkerArgs } from "@playwright/test";
import { E2E_USER, headersFor } from "./env";

type Pw = PlaywrightWorkerArgs["playwright"];

/** A request context that arrives as `user` (the proxy headers of that user). The caller disposes it. */
export function asUser(playwright: Pw, baseURL: string | undefined, user: string): Promise<APIRequestContext> {
  return playwright.request.newContext({ baseURL, extraHTTPHeaders: headersFor(user) });
}

export const asOwner = (playwright: Pw, baseURL: string | undefined) => asUser(playwright, baseURL, E2E_USER);

/** Discards (or restores to draft) a take as the Owner. */
export async function setStatus(owner: APIRequestContext, id: string, status: "discarded" | "draft"): Promise<void> {
  const r = await owner.patch(`/api/tracks/${id}`, { data: { status } });
  if (!r.ok()) throw new Error(`PATCH /api/tracks/${id} -> ${r.status()} ${await r.text()}`);
}

export interface RatingBody {
  mine: { stars: number | null } | null;
  others?: { user: string; stars: number | null; former?: boolean }[] | null;
  average?: unknown;
}

/** GET /api/tracks/{id} rating block as the caller. */
export async function ratingOf(ctx: APIRequestContext, id: string): Promise<RatingBody> {
  const r = await ctx.get(`/api/tracks/${id}`);
  if (!r.ok()) throw new Error(`GET /api/tracks/${id} -> ${r.status()}`);
  return ((await r.json()) as { rating: RatingBody }).rating;
}
