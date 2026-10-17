// A restored queue (playerStore, D2) keeps only its source label and href. This rebuilds the "load more" function from the
// href for the two list sources that page: the New inbox (/new) and the Library (/ with its filters). Anything else
// (a Song, a playlist, the Judge list) restores as a plain finite queue.
import type { components } from "../api/schema";
import { newSince } from "./inbox";
import { parseFilters, toApiQuery, type SearchQuery } from "./libraryQuery";

type TrackPage = components["schemas"]["TrackPage"];

export const MAX_RESTORE_PAGES = 8;
const PAGE = 50;

export interface SourceDeps {
  /** One search_tracks page. */
  search(query: SearchQuery): Promise<TrackPage | null>;
  /** The New inbox's seen marker (recorded_from). */
  seenAt(): string;
  /** Ids already in the player queue. */
  queue(): readonly string[];
}

/** The base query of a list source, or null when the source does not page. */
export function sourceQuery(href: string, seenAt: string): { query: SearchQuery; fresh?: string } | null {
  const q = href.indexOf("?");
  const path = q < 0 ? href : href.slice(0, q);
  const search = q < 0 ? "" : href.slice(q);
  if (path === "/new") return { query: { recorded_from: seenAt, sort: "recorded_asc", limit: PAGE }, fresh: seenAt };
  if (path === "/" || path === "/library") return { query: { ...toApiQuery(parseFilters(search)), limit: PAGE } };
  return null;
}

/**
 * The lazy `more()` of a restored queue: the first call walks the source from its first page and returns the ids the
 * queue does not have yet (skipping pages that are entirely in it, at most MAX_RESTORE_PAGES); later calls continue
 * from where it stopped. An empty answer means the source is used up.
 */
export function restoreMore(href: string, deps: SourceDeps): (() => Promise<string[]>) | null {
  const base = sourceQuery(href, deps.seenAt());
  if (!base) return null;
  let cursor: string | undefined;
  let done = false;
  return async () => {
    for (let i = 0; i < MAX_RESTORE_PAGES && !done; i++) {
      const page = await deps.search({ ...base.query, ...(cursor ? { cursor } : {}) });
      if (!page) throw new Error("page failed");
      cursor = page.next_cursor ?? undefined;
      if (!cursor) done = true;
      const items = base.fresh ? newSince(page.items, base.fresh) : page.items;
      const have = new Set(deps.queue());
      const ids = items.map((t) => t.id).filter((id) => !have.has(id));
      if (ids.length > 0) return ids;
    }
    return [];
  };
}
