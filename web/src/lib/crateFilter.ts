// The Crate filter vocabulary (mm.crate-filter/1, C125.2) for the SPA. The keys of a saved filter are the query
// parameters of the matching list operation minus the non-saved set. openapi.json is not readable at runtime, so the
// field tables below are typed `satisfies Record<key, ...>` over the generated query types: a new list-op parameter is a
// compile error until it is added to a table or to NON_SAVED, and crateFilter.test.ts compares the key sets with
// openapi.json (the TypeScript twin of TestCrateVocabulary_EqualsListOps).
import type { operations } from "../api/schema";

export type Subject = "song" | "take";
export type FilterValue = string | number | boolean | string[];
/** A saved filter: a JSON object of vocabulary keys. */
export type CrateFilter = Record<string, FilterValue>;

type SongQuery = NonNullable<operations["list_songs"]["parameters"]["query"]>;
type TakeQuery = NonNullable<operations["search_tracks"]["parameters"]["query"]>;

/** Parameters of the list operations a saved filter never carries. */
export const NON_SAVED = ["limit", "cursor", "sort", "include_hidden", "include_archived", "draft_id", "track_id", "song_id", "root_track_id", "crate_id"] as const;
type NonSaved = (typeof NON_SAVED)[number];

export type FieldKind = "bool" | "int" | "number" | "string" | "strings" | "instant";
export interface FieldSpec {
  kind: FieldKind;
  label: string;
  group: "Basics" | "Sound" | "Listening" | "Length" | "Rating" | "Pick" | "Library";
  enum?: readonly string[];
  maxItems?: number;
  min?: number;
  max?: number;
  hint?: string;
}
const f = (kind: FieldKind, label: string, group: FieldSpec["group"], rest: Partial<FieldSpec> = {}): FieldSpec => ({ kind, label, group, ...rest });

const SONG_STATES = ["draft", "rendered", "judged", "picked"] as const;
const TAKE_STATUS = ["draft", "to_relisten", "final", "discarded"] as const;

// Order = order in the editor (and of the keys of a built filter).
export const SONG_FIELDS = {
  q: f("string", "Search text", "Basics"),
  state: f("strings", "State", "Basics", { enum: SONG_STATES, maxItems: 4 }),
  muse_id: f("string", "Muse id", "Basics"),
  no_muse: f("bool", "Has no Muse", "Basics"),
  angle: f("string", "Angle", "Basics"),
  session_id: f("string", "Session id", "Basics"),
  queued: f("bool", "Queued for generation", "Basics"),
  sound: f("strings", "Sound (facet:value)", "Sound", { maxItems: 30, hint: "e.g. genre_family:reggae" }),
  tempo_mismatch: f("bool", "Tempo mismatch", "Sound"),
  unheard: f("bool", "Unheard", "Listening", { hint: "no listen by you or a device" }),
  min_duration_s: f("number", "Min duration (s)", "Length", { min: 0 }),
  max_duration_s: f("number", "Max duration (s)", "Length", { min: 0 }),
  min_stars: f("int", "Min stars", "Rating", { min: 1, max: 5 }),
  unrated: f("bool", "Unrated", "Rating"),
  tag: f("strings", "Tags", "Rating", { maxItems: 5 }),
  has_pick: f("bool", "Has a Pick row (any source)", "Pick"),
  pick_decided: f("bool", "Pick decided (pinned or ladder)", "Pick"),
  has_open_proposals: f("bool", "Has open Pick proposals", "Pick"),
  lossy: f("bool", "Lossy source", "Library"),
  recipe_known: f("bool", "Recipe known", "Library"),
  in_record: f("bool", "In a Record", "Library"),
} as const satisfies Record<Exclude<keyof SongQuery, NonSaved>, FieldSpec>;

export const TAKE_FIELDS = {
  q: f("string", "Search text", "Basics"),
  status: f("strings", "Status", "Basics", { enum: TAKE_STATUS, maxItems: 4 }),
  style: f("strings", "Style terms", "Basics", { maxItems: 5 }),
  session_id: f("string", "Session id", "Basics"),
  sound: f("strings", "Sound (facet:value)", "Sound", { maxItems: 30, hint: "e.g. genre_family:reggae" }),
  unheard: f("bool", "Unheard", "Listening", { hint: "no listen by you or a device" }),
  min_duration_s: f("number", "Min duration (s)", "Length", { min: 0 }),
  max_duration_s: f("number", "Max duration (s)", "Length", { min: 0 }),
  recorded_from: f("instant", "Recorded from", "Library", { hint: "date, YYYY-MM-DD" }),
  recorded_to: f("instant", "Recorded to", "Library", { hint: "date, YYYY-MM-DD" }),
  min_stars: f("int", "Min stars", "Rating", { min: 1, max: 5 }),
  unrated: f("bool", "Unrated", "Rating"),
  tag: f("strings", "Tags", "Rating", { maxItems: 5 }),
  is_pick: f("bool", "Is the Pick of its Song", "Pick"),
  has_score: f("bool", "Has a score", "Library"),
  truncated: f("bool", "Truncated", "Library"),
  has_open_comments: f("bool", "Has open comments", "Library"),
  recipe_known: f("bool", "Recipe known", "Library"),
  lossy: f("bool", "Lossy source", "Library"),
  in_record: f("bool", "In a Record", "Library"),
} as const satisfies Record<Exclude<keyof TakeQuery, NonSaved>, FieldSpec>;

export const MAX_FILTER_BYTES = 4096;

export function fieldTable(subject: Subject): Readonly<Record<string, FieldSpec>> {
  return subject === "song" ? SONG_FIELDS : TAKE_FIELDS;
}
export function fieldKeys(subject: Subject): string[] {
  return Object.keys(fieldTable(subject));
}

function isEmpty(v: unknown): boolean {
  return v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0);
}

/** A filter in the table's key order with unset values dropped (an empty array is never saved: C125.4). */
export function normalizeFilter(subject: Subject, raw: Record<string, unknown>): CrateFilter {
  const out: CrateFilter = {};
  for (const [k, spec] of Object.entries(fieldTable(subject))) {
    const v = raw[k];
    if (isEmpty(v)) continue;
    if (spec.kind === "strings") out[k] = (Array.isArray(v) ? v : [v]).map(String).filter((s) => s !== "");
    else out[k] = v as FilterValue;
    if (Array.isArray(out[k]) && (out[k] as string[]).length === 0) delete out[k];
  }
  return out;
}

/** Keys a saved filter carries that the vocabulary does not know (a stored filter from a newer server). */
export function unknownKeys(subject: Subject, raw: Record<string, unknown>): string[] {
  const t = fieldTable(subject);
  return Object.keys(raw).filter((k) => !(k in t));
}

// ---- validation (client side, the server's wording) ----

export type FilterErrors = Record<string, string>;

export function validateFilter(subject: Subject, filter: Record<string, unknown>): FilterErrors {
  const errs: FilterErrors = {};
  const t = fieldTable(subject);
  for (const [k, v] of Object.entries(filter)) {
    const spec = t[k];
    if (!spec) {
      errs[k] = "unknown filter key";
      continue;
    }
    if (typeof v === "number") {
      if (spec.min !== undefined && v < spec.min) errs[k] = spec.max !== undefined ? `must be ${spec.min}..${spec.max}` : "must not be negative";
      else if (spec.max !== undefined && v > spec.max) errs[k] = `must be ${spec.min ?? 0}..${spec.max}`;
      else if (spec.kind === "int" && !Number.isInteger(v)) errs[k] = "must be a whole number";
    }
    if (Array.isArray(v) && spec.maxItems !== undefined && v.length > spec.maxItems) errs[k] = `at most ${spec.maxItems} ${k === "tag" ? "tags" : "values"}`;
  }
  if (filter["min_stars"] !== undefined && filter["unrated"] === true) errs["unrated"] = "cannot be combined with min_stars";
  const lo = filter["min_duration_s"];
  const hi = filter["max_duration_s"];
  if (typeof lo === "number" && typeof hi === "number" && lo > hi) errs["max_duration_s"] = "must not be below min_duration_s";
  if (Object.keys(errs).length === 0 && new TextEncoder().encode(JSON.stringify(filter)).length > MAX_FILTER_BYTES) errs["filter"] = `at most ${MAX_FILTER_BYTES} bytes`;
  return errs;
}

// ---- filter <-> query string / list-op query ----

/** `?key=v&key=v2`: arrays repeat the key, booleans are true/false (the list ops' explode style). */
export function filterToSearch(filter: CrateFilter): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(filter)) {
    if (Array.isArray(v)) for (const x of v) p.append(k, x);
    else p.set(k, String(v));
  }
  return p.toString();
}

export function filterFromSearch(subject: Subject, search: string): CrateFilter {
  const p = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const raw: Record<string, unknown> = {};
  for (const [k, spec] of Object.entries(fieldTable(subject))) {
    const all = p.getAll(k);
    if (all.length === 0) continue;
    switch (spec.kind) {
      case "strings":
        raw[k] = [...new Set(all.filter((s) => s !== ""))];
        break;
      case "bool":
        if (all[0] === "true" || all[0] === "false") raw[k] = all[0] === "true";
        break;
      case "int":
      case "number": {
        const n = Number(all[0]);
        if (all[0] !== "" && Number.isFinite(n)) raw[k] = n;
        break;
      }
      default:
        raw[k] = all[0];
    }
  }
  return normalizeFilter(subject, raw);
}

/** The list_songs / search_tracks query for a filter (the same parameters, so the server's one code path answers). */
export function filterToQuery(filter: CrateFilter, extra: { limit?: number; cursor?: string } = {}): Record<string, FilterValue> {
  const q: Record<string, FilterValue> = {};
  for (const [k, v] of Object.entries(filter)) q[k] = Array.isArray(v) ? [...v] : v;
  if (extra.limit !== undefined) q["limit"] = extra.limit;
  if (extra.cursor) q["cursor"] = extra.cursor;
  return q;
}

// ---- form model: text the controls edit ----

export type FormValue = string | string[];
export type FormModel = Record<string, FormValue>;

export function formFromFilter(subject: Subject, filter: Record<string, unknown>): FormModel {
  const m: FormModel = {};
  for (const [k, spec] of Object.entries(fieldTable(subject))) {
    const v = filter[k];
    if (spec.kind === "strings") m[k] = Array.isArray(v) ? v.map(String) : [];
    else m[k] = v === undefined || v === null ? "" : String(v);
  }
  return m;
}

/** The filter a form describes. Bool controls are "" (any) | "true" | "false"; numbers are text so half-typed input survives. */
export function filterFromForm(subject: Subject, form: FormModel): CrateFilter {
  const raw: Record<string, unknown> = {};
  for (const [k, spec] of Object.entries(fieldTable(subject))) {
    const v = form[k];
    if (v === undefined) continue;
    switch (spec.kind) {
      case "strings":
        raw[k] = Array.isArray(v) ? v.map((s) => s.trim()).filter(Boolean) : [];
        break;
      case "bool":
        if (v === "true" || v === "false") raw[k] = v === "true";
        break;
      case "int":
      case "number": {
        const s = String(v).trim();
        if (s !== "") raw[k] = Number(s);
        break;
      }
      default: {
        const s = String(v).trim();
        if (s !== "") raw[k] = s;
      }
    }
  }
  return normalizeFilter(subject, raw);
}

/** Splits free text ("a, b  c") into list entries for a `strings` field. */
export function splitList(text: string): string[] {
  return [...new Set(text.split(/[,\n]/).map((s) => s.trim()).filter(Boolean))];
}

// ---- merge preview (C125.4) ----

const LIST_KEYS = new Set(["state", "status", "style", "tag", "sound", "origin"]);

export interface MergeResult {
  merged: CrateFilter;
  /** Fields set in both with different scalar values (the server answers 422 FILTER_CONFLICT). */
  conflicts: string[];
  /** A list field whose intersection is empty: it matches nothing. */
  none: boolean;
}

/** AND semantics of `Expand`: list fields intersect, equal values stay, different scalars conflict. */
export function mergeFilters(stored: CrateFilter, request: CrateFilter): MergeResult {
  const merged: CrateFilter = { ...stored };
  const conflicts: string[] = [];
  let none = false;
  for (const [k, v] of Object.entries(request)) {
    const s = stored[k];
    if (s === undefined) {
      merged[k] = v;
    } else if (LIST_KEYS.has(k) && Array.isArray(s) && Array.isArray(v)) {
      const both = s.filter((x) => v.includes(x));
      if (both.length === 0) none = true;
      merged[k] = both;
    } else if (JSON.stringify(s) !== JSON.stringify(v)) {
      conflicts.push(k);
    }
  }
  return { merged, conflicts, none };
}

// ---- display ----

/** One short phrase per key, for the Crate list ("unheard, ≥ 240 s, sound genre_family:reggae"). */
export function describeFilter(subject: Subject, filter: Record<string, unknown>): string[] {
  const t = fieldTable(subject);
  const out: string[] = [];
  for (const [k, v] of Object.entries(normalizeFilter(subject, filter))) {
    const spec = t[k]!;
    if (spec.kind === "bool") out.push(v === true ? spec.label : `not: ${spec.label}`);
    else if (Array.isArray(v)) out.push(`${spec.label}: ${v.join(" | ")}`);
    else out.push(`${spec.label}: ${v}`);
  }
  return out;
}

export function emptyFilterHint(subject: Subject): string {
  return subject === "song" ? "Every Song" : "Every Take";
}
