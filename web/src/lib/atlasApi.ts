// Typed wrappers for the Atlas page (C145.2, C145.7): the paged load with the stale-restart and pending-refetch
// policies, the adherence read, and the small pure helpers the page and its components share. Network only through
// the generated client (`atlasGetter`, `fetchAdherence`); the policies take an injected getter so bun tests need none.
import type { createApi } from "../api/client";
import type { components, operations } from "../api/schema";
import { MAX_VIEW, isHollow, type AtlasFilter, type AtlasPoint, filterToQuery } from "./atlasView";
import { capForRecord, failure, type Failure } from "./crateApi";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];
export type AtlasPage = S["Atlas"];
export type Adherence = S["Adherence"];
export type AtlasQuery = NonNullable<operations["get_song_atlas"]["parameters"]["query"]>;
export type AdherenceQuery = NonNullable<operations["get_adherence"]["parameters"]["query"]>;

export type AtlasGet = (query: AtlasQuery) => Promise<{ data?: AtlasPage; error?: unknown; response: { status: number } }>;

export function atlasGetter(api: Api): AtlasGet {
  return (query) => api.GET("/api/atlas", { params: { query } });
}

export const PENDING_DELAY_MS = 1500;
export const MAX_PENDING_REFETCH = 3;
export const STALE_DEBOUNCE_MS = 400;
/** Restarts after ATLAS_STALE before the page gives up with "Map changed - reload". */
export const MAX_STALE_RESTARTS = 2;

export interface LoadOptions {
  maxView?: number;
  limit?: number;
  sleep?: (ms: number) => Promise<void>;
  /** True when a newer load superseded this one: the loop stops quietly. */
  cancelled?: () => boolean;
  /** After every page: the points so far (the first call carries the first page). */
  onProgress?: (partial: AtlasPage, loaded: number) => void;
}
export type LoadResult =
  | { kind: "ok"; atlas: AtlasPage; capped: boolean; pendingLeft: boolean }
  | { kind: "stale" }
  | { kind: "failed"; failure: Failure }
  | { kind: "cancelled" };

const realSleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

type Pass = { kind: "ok"; atlas: AtlasPage; capped: boolean } | { kind: "stale" } | { kind: "failed"; failure: Failure } | { kind: "cancelled" };

/**
 * Loads the whole map: follows `next_cursor` to the end or `maxView` points. A 409 ATLAS_STALE restarts from the
 * first page after a short debounce (at most MAX_STALE_RESTARTS times, then "stale"); `pending: true` refetches the
 * whole map after 1.5 s, at most three times. `near_song_id` is sent on the first page only.
 */
export async function loadAtlas(get: AtlasGet, base: AtlasQuery, o: LoadOptions = {}): Promise<LoadResult> {
  const maxView = o.maxView ?? MAX_VIEW;
  const sleep = o.sleep ?? realSleep;
  const cancelled = o.cancelled ?? (() => false);

  async function pass(): Promise<Pass> {
    let cursor: string | undefined;
    let first: AtlasPage | null = null;
    let points: AtlasPage["points"] = [];
    let capped = false;
    for (;;) {
      const q: AtlasQuery = { ...base, ...(o.limit ? { limit: o.limit } : {}) };
      if (cursor) {
        delete q.near_song_id;
        q.cursor = cursor;
      }
      const r = await get(q);
      if (cancelled()) return { kind: "cancelled" };
      if (!r.data) {
        const f = failure(r.response.status, r.error, "Could not load the Atlas");
        return f.status === 409 && f.code === "ATLAS_STALE" ? { kind: "stale" } : { kind: "failed", failure: f };
      }
      first ??= r.data;
      points = points.concat(r.data.points);
      const next = r.data.next_cursor;
      if (points.length >= maxView) {
        capped = next !== null || points.length > maxView;
        points = points.slice(0, maxView);
        o.onProgress?.({ ...first, points }, points.length);
        break;
      }
      o.onProgress?.({ ...first, points }, points.length);
      if (!next) break;
      cursor = next;
    }
    return { kind: "ok", atlas: { ...first, points, next_cursor: null }, capped };
  }

  let restarts = 0;
  let refetches = 0;
  for (;;) {
    const p = await pass();
    if (p.kind === "stale") {
      if (restarts >= MAX_STALE_RESTARTS) return { kind: "stale" };
      restarts++;
      await sleep(STALE_DEBOUNCE_MS);
      if (cancelled()) return { kind: "cancelled" };
      continue;
    }
    if (p.kind !== "ok") return p;
    if (p.atlas.pending && refetches < MAX_PENDING_REFETCH) {
      refetches++;
      await sleep(PENDING_DELAY_MS);
      if (cancelled()) return { kind: "cancelled" };
      continue;
    }
    return { kind: "ok", atlas: p.atlas, capped: p.capped, pendingLeft: p.atlas.pending };
  }
}

// ---- adherence ----

export async function fetchAdherence(api: Api, query: AdherenceQuery = {}): Promise<{ ok: true; value: Adherence } | Failure> {
  try {
    const r = await api.GET("/api/atlas/adherence", { params: { query } });
    return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not load the adherence report");
  } catch {
    return { ok: false, status: 0, code: "NETWORK", message: "Could not load the adherence report: network error", stale: false };
  }
}

// ---- axes, presets, words ----

export const AXES = ["tempo", "energy", "duration", "bpm_requested", "bpm_measured", "family", "vocal", "era", "mood", "language", "key"] as const;
export const COLOR_BYS = ["none", "muse", "state", "pick", "family", "vocal", "era", "mood", "language", "key", "record"] as const;
export const DEFAULT_X = "tempo";
export const DEFAULT_Y = "energy";
export const PRESETS: readonly { name: string; label: string; x: string; y: string }[] = [
  { name: "tempo-energy", label: "Tempo × Energy", x: "tempo", y: "energy" },
  { name: "family-vocal", label: "Family × Vocal", x: "family", y: "vocal" },
  { name: "requested-got", label: "Requested × Got", x: "bpm_requested", y: "bpm_measured" },
];
export const AXIS_PAIR_TEXT = "x and y must be two different axes";
export const PREDICTED_FOOTNOTE = "Predicted energy is estimated from tempo band, genre and mood, so unrendered Songs form stacks; it is not a measurement.";

/** The server's AXIS_PAIR_INVALID text when the pair is equal (blank means the default axis), else "". */
export function axisPairError(x: string, y: string): string {
  return (x || DEFAULT_X) === (y || DEFAULT_Y) ? AXIS_PAIR_TEXT : "";
}

/** The preset whose pair is on screen (blank axes are the default Tempo x Energy). */
export function activePreset(f: Pick<AtlasFilter, "x" | "y">): string {
  const x = f.x || DEFAULT_X;
  const y = f.y || DEFAULT_Y;
  return PRESETS.find((p) => p.x === x && p.y === y)?.name ?? "";
}

/** "443 plotted · 13 measured · 430 estimated · 26 not plotted": estimated = predicted on either plotted axis. */
export function totalsText(t: AtlasPage["totals"]): string {
  const est = Math.min(t.plotted, Math.max(t.basis.x.p, t.basis.y.p));
  const parts = [`${t.plotted} plotted`, `${t.plotted - est} measured`, `${est} estimated`, `${Math.max(0, t.scope - t.plotted)} not plotted`];
  if (t.truncated || t.scope_total > t.scope) parts.push(`${t.scope_total} match, first ${t.scope} used`);
  return parts.join(" · ");
}

/** The points the canvas and the table both show: predicted points are hidden unless asked for (D-L3-14). */
export function visiblePoints(points: readonly AtlasPoint[], hidePredicted: boolean): AtlasPoint[] {
  return hidePredicted ? points.filter((p) => !isHollow(p)) : points.slice();
}

export const isGuideMap = (guides: readonly { kind: string }[]): boolean => guides.length > 0;

/** "Add to Record" / "New Record from selection": at most 60 Songs, the note says what was left out. */
export function recordHandOff(ids: readonly string[]): { songIds: string[]; note: string } {
  const c = capForRecord(ids);
  return { songIds: c.ids, note: c.note };
}
export const sequenceHref = (recordId: string): string => `/records/${encodeURIComponent(recordId)}?sequence=arc`;

/** "Show on the Board": the Board filters the Atlas page shares, plus the one Muse the selection shares (or the page's own). */
export function boardHref(f: AtlasFilter, selectionMuse = ""): string {
  const p = new URLSearchParams();
  if (f.session) p.set("session", f.session);
  for (const s of f.states) p.append("state", s);
  for (const s of f.sounds) p.append("sound", s);
  if (f.q) p.set("q", f.q);
  const muse = selectionMuse || f.muse;
  if (muse) p.set("muse", muse);
  const s = p.toString();
  return "/board" + (s ? "?" + s : "");
}

/** The Muse every selected point shares when the map is coloured by Muse (`mi` indexes `muses`), else "". */
export function sharedMuse(points: readonly AtlasPoint[], muses: readonly string[], selected: ReadonlySet<string>): string {
  let found: number | undefined;
  for (const p of points) {
    if (!selected.has(p.id)) continue;
    if (p.mi === undefined) return "";
    if (found !== undefined && found !== p.mi) return "";
    found = p.mi;
  }
  return found === undefined ? "" : (muses[found] ?? "");
}

/** The canvas and table selection gestures: toggle flips each id, add unions, clear empties. */
export function applySelect(selected: ReadonlySet<string>, ids: readonly string[], mode: "toggle" | "add" | "clear"): Set<string> {
  if (mode === "clear") return new Set();
  const next = new Set(selected);
  for (const id of ids) {
    if (mode === "add" || !next.has(id)) next.add(id);
    else next.delete(id);
  }
  return next;
}

/** The route signals its first data once, however many loads follow. */
export function readyOnce(signal: () => void): () => void {
  let done = false;
  return () => {
    if (done) return;
    done = true;
    signal();
  };
}

export { filterToQuery };
