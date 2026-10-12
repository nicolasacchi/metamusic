// Library filters <-> URL query string (back/forward restores filters) and the search_tracks params.
import type { operations } from "../api/schema";

export type SearchQuery = NonNullable<operations["search_tracks"]["parameters"]["query"]>;
export type Sort = NonNullable<SearchQuery["sort"]>;
export type Status = NonNullable<SearchQuery["status"]>[number];

export const SORTS: readonly Sort[] = ["recorded_desc", "recorded_asc", "duration_desc", "duration_asc", "title_asc", "relevance"];
export const STATUSES: readonly Status[] = ["draft", "to_relisten", "final", "discarded"];
export const MAX_STYLE_TERMS = 5;

export interface Filters {
  q: string;
  status: Status[];
  style: string[];
  minDuration: number | null;
  maxDuration: number | null;
  recordedFrom: string;
  recordedTo: string;
  hasScore: boolean | null;
  truncated: boolean | null;
  sort: Sort;
}

export function defaultFilters(): Filters {
  return {
    q: "",
    status: [],
    style: [],
    minDuration: null,
    maxDuration: null,
    recordedFrom: "",
    recordedTo: "",
    hasScore: null,
    truncated: null,
    sort: "recorded_desc",
  };
}

/** The server's default sort: relevance when q is set, else newest first. */
export function defaultSort(q: string): Sort {
  return q ? "relevance" : "recorded_desc";
}

function num(v: string | null): number | null {
  if (v === null || v.trim() === "") return null;
  const n = Number(v);
  return Number.isFinite(n) && n >= 0 ? n : null;
}

function bool(v: string | null): boolean | null {
  return v === "true" ? true : v === "false" ? false : null;
}

export function parseFilters(search: string | URLSearchParams): Filters {
  const p = typeof search === "string" ? new URLSearchParams(search) : search;
  const f = defaultFilters();
  f.q = (p.get("q") ?? "").slice(0, 200);
  f.status = [...new Set(p.getAll("status"))].filter((s): s is Status => (STATUSES as readonly string[]).includes(s));
  f.style = [...new Set(p.getAll("style").filter(Boolean))].slice(0, MAX_STYLE_TERMS);
  f.minDuration = num(p.get("min_duration_s"));
  f.maxDuration = num(p.get("max_duration_s"));
  f.recordedFrom = p.get("recorded_from") ?? "";
  f.recordedTo = p.get("recorded_to") ?? "";
  f.hasScore = bool(p.get("has_score"));
  f.truncated = bool(p.get("truncated"));
  const sort = p.get("sort");
  f.sort = sort && (SORTS as readonly string[]).includes(sort) ? (sort as Sort) : defaultSort(f.q);
  return f;
}

function pairs(f: Filters): [string, string][] {
  const out: [string, string][] = [];
  if (f.q) out.push(["q", f.q]);
  for (const s of f.status) out.push(["status", s]);
  for (const s of f.style) out.push(["style", s]);
  if (f.minDuration !== null) out.push(["min_duration_s", String(f.minDuration)]);
  if (f.maxDuration !== null) out.push(["max_duration_s", String(f.maxDuration)]);
  if (f.recordedFrom) out.push(["recorded_from", f.recordedFrom]);
  if (f.recordedTo) out.push(["recorded_to", f.recordedTo]);
  if (f.hasScore !== null) out.push(["has_score", String(f.hasScore)]);
  if (f.truncated !== null) out.push(["truncated", String(f.truncated)]);
  if (f.sort !== defaultSort(f.q)) out.push(["sort", f.sort]);
  return out;
}

/** "?a=b&..." or "" when every filter is at its default. */
export function toSearch(f: Filters): string {
  const p = new URLSearchParams(pairs(f));
  const s = p.toString();
  return s ? "?" + s : "";
}

/** search_tracks query parameters; defaults omitted so the server applies its own. */
export function toApiQuery(f: Filters): SearchQuery {
  const q: SearchQuery = {};
  if (f.q) q.q = f.q;
  if (f.status.length) q.status = f.status;
  if (f.style.length) q.style = f.style;
  if (f.minDuration !== null) q.min_duration_s = f.minDuration;
  if (f.maxDuration !== null) q.max_duration_s = f.maxDuration;
  if (f.recordedFrom) q.recorded_from = f.recordedFrom;
  if (f.recordedTo) q.recorded_to = f.recordedTo;
  if (f.hasScore !== null) q.has_score = f.hasScore;
  if (f.truncated !== null) q.truncated = f.truncated;
  if (f.sort !== defaultSort(f.q)) q.sort = f.sort;
  return q;
}
