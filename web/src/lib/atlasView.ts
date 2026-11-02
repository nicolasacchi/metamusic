// The Atlas view model (C145.7): axis -> screen transforms, ticks, the spatial-grid hit test, keyboard
// navigation, rectangle selection, the legend, and the filter <-> query string mapping. Pure functions; the
// canvas component and the page own the state. Points arrive with x and y already laid out in [0, 1] by the
// server, so the transforms here only place that unit square on the screen (zoom and pan).
import type { components } from "../api/schema";
import { capForRecord } from "./crateApi";
import { paletteVar } from "./palette";

type S = components["schemas"];
export type AtlasPoint = S["Point"];
export type Axis = S["AxisInfo"];
export type AxisPair = S["AxisPair"];
export type LegendEntry = S["LegendEntry"];
type AtlasQuery = NonNullable<import("../api/schema").operations["get_song_atlas"]["parameters"]["query"]>;

export const MIN_ZOOM = 1;
export const MAX_ZOOM = 16;
/** Hit radius (CSS px): a touch point is a 44 px target. */
export const HIT_RADIUS = { touch: 22, mouse: 12 } as const;
export const MAX_VIEW = 5000;

// --- axes ---------------------------------------------------------------------------------------------

/** Display units per fixed-point unit: tempo and duration are centi-units, energy is x 10 000. */
function unitScale(a: Axis): number {
  if (a.name === "energy") return 10000;
  if (a.unit === "BPM" || a.unit === "s") return 100;
  return 1;
}

function numericU(a: Axis, fixed: number): number {
  const lo = a.min ?? 0;
  const hi = a.max ?? 0;
  const v = Math.min(Math.max(fixed, lo), hi);
  if (hi <= lo) return 0;
  if (a.scale === "log2") return (Math.log2(v) - Math.log2(lo)) / (Math.log2(hi) - Math.log2(lo));
  return (v - lo) / (hi - lo);
}

/** Position in [0, 1] of a numeric axis value given in display units (60 BPM, 0.5 energy, 120 s). */
export function displayToU(a: Axis, display: number): number {
  return numericU(a, display * unitScale(a));
}

function uToFixed(a: Axis, u: number): number {
  const lo = a.min ?? 0;
  const hi = a.max ?? 0;
  const t = Math.min(Math.max(u, 0), 1);
  if (a.scale === "log2" && lo > 0) return 2 ** (Math.log2(lo) + t * (Math.log2(hi) - Math.log2(lo)));
  return lo + t * (hi - lo);
}

export interface Tick {
  u: number;
  label: string;
}

const TEMPO_TICKS = [60, 80, 100, 120, 160, 200];

function fmt(n: number): string {
  return String(Math.round(n * 1000) / 1000);
}

/** Tick positions and labels: log ticks for tempo, band centres for categorical axes. */
export function axisTicks(a: Axis): Tick[] {
  if (a.kind === "categorical") {
    return (a.categories ?? []).map((c) => ({ u: (c.lo + c.hi) / 2, label: c.value }));
  }
  const k = unitScale(a);
  const lo = (a.min ?? 0) / k;
  const hi = (a.max ?? 0) / k;
  let values: number[];
  if (a.scale === "log2") values = TEMPO_TICKS.filter((t) => t >= lo - 1e-9 && t <= hi + 1e-9);
  else if (a.unit === "s") {
    values = [];
    for (let s = Math.ceil(lo / 60) * 60; s <= hi + 1e-9; s += 60) values.push(s);
  } else values = [0, 1, 2, 3, 4].map((i) => lo + ((hi - lo) * i) / 4);
  return values.map((v) => ({ u: displayToU(a, v), label: a.unit === "s" ? `${fmt(v / 60)}m` : fmt(v) }));
}

/** The (approximate: the layout nudges stacked points) axis value at position u, as text. */
export function valueLabel(a: Axis, u: number): string {
  if (a.kind === "categorical") {
    const cats = a.categories ?? [];
    const c = cats.find((x) => u >= x.lo && u < x.hi) ?? cats[cats.length - 1];
    return c?.value ?? "";
  }
  const v = uToFixed(a, u) / unitScale(a);
  if (a.unit === "BPM") return `${Math.round(v)} BPM`;
  if (a.unit === "s") return `${Math.round(v)} s`;
  return v.toFixed(2);
}

// --- view: zoom and pan --------------------------------------------------------------------------------

export interface View {
  zoom: number;
  /** Centre of the visible window in map units. */
  cx: number;
  cy: number;
}
export interface Plot {
  left: number;
  top: number;
  width: number;
  height: number;
}

export const HOME_VIEW: View = { zoom: 1, cx: 0.5, cy: 0.5 };

function clampView(v: View): View {
  const zoom = Math.min(Math.max(v.zoom, MIN_ZOOM), MAX_ZOOM);
  const half = 0.5 / zoom;
  return { zoom, cx: Math.min(Math.max(v.cx, half), 1 - half), cy: Math.min(Math.max(v.cy, half), 1 - half) };
}

/** Map unit (u right, v up) -> screen px. */
export function project(view: View, plot: Plot, u: number, v: number): [number, number] {
  return [plot.left + ((u - view.cx) * view.zoom + 0.5) * plot.width, plot.top + (0.5 - (v - view.cy) * view.zoom) * plot.height];
}

export function unproject(view: View, plot: Plot, sx: number, sy: number): [number, number] {
  return [view.cx + ((sx - plot.left) / plot.width - 0.5) / view.zoom, view.cy - ((sy - plot.top) / plot.height - 0.5) / view.zoom];
}

/** Zoom by `factor` keeping the map point under (sx, sy) fixed. */
export function zoomAt(view: View, plot: Plot, factor: number, sx: number, sy: number): View {
  const [u, v] = unproject(view, plot, sx, sy);
  const zoom = Math.min(Math.max(view.zoom * factor, MIN_ZOOM), MAX_ZOOM);
  return clampView({ zoom, cx: u - ((sx - plot.left) / plot.width - 0.5) / zoom, cy: v + ((sy - plot.top) / plot.height - 0.5) / zoom });
}

/** Drag by (dx, dy) screen px: the map follows the finger. */
export function panBy(view: View, plot: Plot, dx: number, dy: number): View {
  return clampView({ zoom: view.zoom, cx: view.cx - dx / (plot.width * view.zoom), cy: view.cy + dy / (plot.height * view.zoom) });
}

// --- model ---------------------------------------------------------------------------------------------

export interface AtlasModel {
  points: AtlasPoint[];
  us: Float64Array;
  vs: Float64Array;
  /** 1 when the point is predicted on either plotted axis (drawn hollow). */
  hollow: Uint8Array;
  /** 1 when the server marked the point as outside the filter (h = 0 with dim). */
  dim: Uint8Array;
  hiddenPredicted: number;
}

/** Predicted values are estimates (D-L3-14): a point predicted on either axis is hollow. */
export function isHollow(p: AtlasPoint): boolean {
  return p.xb === "p" || p.yb === "p";
}

export function buildModel(points: readonly AtlasPoint[], opts: { hidePredicted: boolean }): AtlasModel {
  const kept = opts.hidePredicted ? points.filter((p) => !isHollow(p)) : points.slice();
  const n = kept.length;
  const us = new Float64Array(n);
  const vs = new Float64Array(n);
  const hollow = new Uint8Array(n);
  const dim = new Uint8Array(n);
  for (let i = 0; i < n; i++) {
    const p = kept[i]!;
    us[i] = p.x;
    vs[i] = p.y;
    hollow[i] = isHollow(p) ? 1 : 0;
    dim[i] = p.h === 0 ? 1 : 0;
  }
  return { points: kept, us, vs, hollow, dim, hiddenPredicted: points.length - n };
}

export function projectAll(m: AtlasModel, view: View, plot: Plot): { xs: Float64Array; ys: Float64Array } {
  const n = m.us.length;
  const xs = new Float64Array(n);
  const ys = new Float64Array(n);
  const sx = view.zoom * plot.width;
  const sy = view.zoom * plot.height;
  const ox = plot.left + (0.5 - view.cx * view.zoom) * plot.width;
  const oy = plot.top + (0.5 + view.cy * view.zoom) * plot.height;
  for (let i = 0; i < n; i++) {
    xs[i] = ox + m.us[i]! * sx;
    ys[i] = oy - m.vs[i]! * sy;
  }
  return { xs, ys };
}

// --- spatial grid --------------------------------------------------------------------------------------

export interface Grid {
  cell: number;
  cells: Map<number, number[]>;
}

const KEY = 1 << 16;
const cellKey = (cx: number, cy: number) => (cx + 4096) * KEY + (cy + 4096);

export function buildGrid(xs: ArrayLike<number>, ys: ArrayLike<number>, cell: number): Grid {
  const cells = new Map<number, number[]>();
  for (let i = 0; i < xs.length; i++) {
    const k = cellKey(Math.floor(xs[i]! / cell), Math.floor(ys[i]! / cell));
    const a = cells.get(k);
    if (a) a.push(i);
    else cells.set(k, [i]);
  }
  return { cell, cells };
}

/** Index of the nearest point within `radius` px of (x, y), or -1. */
export function hitTest(g: Grid, xs: ArrayLike<number>, ys: ArrayLike<number>, x: number, y: number, radius: number): number {
  const c0 = Math.floor((x - radius) / g.cell);
  const c1 = Math.floor((x + radius) / g.cell);
  const r0 = Math.floor((y - radius) / g.cell);
  const r1 = Math.floor((y + radius) / g.cell);
  let best = -1;
  let bestD = radius * radius;
  for (let cx = c0; cx <= c1; cx++) {
    for (let cy = r0; cy <= r1; cy++) {
      const a = g.cells.get(cellKey(cx, cy));
      if (!a) continue;
      for (const i of a) {
        const dx = xs[i]! - x;
        const dy = ys[i]! - y;
        const d = dx * dx + dy * dy;
        if (d <= bestD && (best < 0 || d < bestD || i < best)) {
          best = i;
          bestD = d;
        }
      }
    }
  }
  return best;
}

// --- keyboard and rectangle ----------------------------------------------------------------------------

export type Dir = "left" | "right" | "up" | "down";

/** The nearest point strictly in direction `dir` of point `from` (in map space; in-line points beat diagonal ones). */
export function nearestInDirection(m: AtlasModel, from: number, dir: Dir): number {
  const u0 = m.us[from]!;
  const v0 = m.vs[from]!;
  let best = -1;
  let bestCost = Infinity;
  for (let i = 0; i < m.us.length; i++) {
    if (i === from) continue;
    const du = m.us[i]! - u0;
    const dv = m.vs[i]! - v0;
    const along = dir === "right" ? du : dir === "left" ? -du : dir === "up" ? dv : -dv;
    if (along <= 1e-9) continue;
    const perp = Math.abs(dir === "left" || dir === "right" ? dv : du);
    const cost = along + 2 * perp;
    if (cost < bestCost) {
      bestCost = cost;
      best = i;
    }
  }
  return best;
}

export interface Rect {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
}

/** Indices (ascending) of the points whose centre lies inside the rectangle, in any corner order. */
export function rectSelect(xs: ArrayLike<number>, ys: ArrayLike<number>, r: Rect): number[] {
  const [xa, xb] = r.x0 <= r.x1 ? [r.x0, r.x1] : [r.x1, r.x0];
  const [ya, yb] = r.y0 <= r.y1 ? [r.y0, r.y1] : [r.y1, r.y0];
  const out: number[] = [];
  for (let i = 0; i < xs.length; i++) if (xs[i]! >= xa && xs[i]! <= xb && ys[i]! >= ya && ys[i]! <= yb) out.push(i);
  return out;
}

/** The Song ids a "New Record from selection" takes: at most 60, with a note about the rest. */
export function selectionCap(ids: readonly string[]): { ids: string[]; note: string } {
  return capForRecord(ids);
}

// --- legend, words, table ------------------------------------------------------------------------------

export interface LegendRow {
  c: number;
  value: string;
  count: number;
  cssVar: string;
}

export function legendEntries(legend: readonly LegendEntry[]): LegendRow[] {
  return legend.map((e) => ({ c: e.c, value: e.value, count: e.count, cssVar: paletteVar(e.c) }));
}

const STATE = ["draft", "rendered", "judged", "picked"] as const;
const PICK = ["none", "newest", "stars", "ladder", "pinned"] as const;
const BASIS = { m: "measured", d: "declared", l: "plan", p: "predicted" } as const;

export const stateWord = (st: number): string => STATE[st] ?? "unknown";
export const pickWord = (pk: number): string => PICK[pk] ?? "unknown";
export const basisWord = (b: AtlasPoint["xb"]): string => (b ? BASIS[b] : "unknown basis");

export interface PointLines {
  title: string;
  state: string;
  pick: string;
  x: string;
  y: string;
}

/** The tooltip text of a point. Values are read back from the layout, so they are approximate for stacked points. */
export function pointLines(p: AtlasPoint, axes: AxisPair): PointLines {
  return {
    title: p.t,
    state: stateWord(p.st),
    pick: pickWord(p.pk),
    x: `${valueLabel(axes.x, p.x)} (${basisWord(p.xb)})`,
    y: `${valueLabel(axes.y, p.y)} (${basisWord(p.yb)})`,
  };
}

export interface TableRow {
  id: string;
  title: string;
  x: string;
  y: string;
  basis: string;
  state: string;
  pick: string;
  colour: string;
}

export function tableRows(points: readonly AtlasPoint[], axes: AxisPair, legend: readonly LegendEntry[]): TableRow[] {
  return points.map((p) => ({
    id: p.id,
    title: p.t,
    x: valueLabel(axes.x, p.x),
    y: valueLabel(axes.y, p.y),
    basis: `${basisWord(p.xb)} / ${basisWord(p.yb)}`,
    state: stateWord(p.st),
    pick: pickWord(p.pk),
    colour: legend.find((l) => l.c === p.c)?.value ?? "",
  }));
}

// --- filter <-> query string ---------------------------------------------------------------------------

export const SONG_STATES = ["draft", "rendered", "judged", "picked"] as const;
export type SongState = (typeof SONG_STATES)[number];

export interface AtlasFilter {
  x: string;
  y: string;
  color: string;
  dim: boolean;
  muse: string;
  session: string;
  states: SongState[];
  /** `facet:value` selections; repeated `sound=` keys. */
  sounds: string[];
  q: string;
  crate: string;
  near: string;
}

export function filterFromSearch(search: string): AtlasFilter {
  const p = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const dim = p.get("dim");
  return {
    x: p.get("x") ?? "",
    y: p.get("y") ?? "",
    color: p.get("color") ?? "",
    dim: dim === "1" || dim === "true",
    muse: p.get("muse") ?? "",
    session: p.get("session") ?? "",
    states: [...new Set(p.getAll("state"))].filter((s): s is SongState => (SONG_STATES as readonly string[]).includes(s)),
    sounds: [...new Set(p.getAll("sound"))].filter(Boolean),
    q: p.get("q") ?? "",
    crate: p.get("crate") ?? "",
    near: p.get("near") ?? "",
  };
}

/** Query string (no leading `?`) for the page URL. */
export function filterToSearch(f: AtlasFilter): string {
  const p = new URLSearchParams();
  if (f.x) p.set("x", f.x);
  if (f.y) p.set("y", f.y);
  if (f.color) p.set("color", f.color);
  if (f.dim) p.set("dim", "1");
  if (f.muse) p.set("muse", f.muse);
  if (f.session) p.set("session", f.session);
  for (const s of f.states) p.append("state", s);
  for (const s of f.sounds) p.append("sound", s);
  if (f.q) p.set("q", f.q);
  if (f.crate) p.set("crate", f.crate);
  if (f.near) p.set("near", f.near);
  return p.toString();
}

/** The get_song_atlas query for these filters (paging parameters are the caller's). */
export function filterToQuery(f: AtlasFilter): AtlasQuery {
  const q: AtlasQuery = {};
  if (f.x) q.x = f.x;
  if (f.y) q.y = f.y;
  if (f.color) q.color_by = f.color;
  if (f.dim) q.dim = true;
  if (f.muse) q.muse_id = f.muse;
  if (f.session) q.session_id = f.session;
  if (f.states.length) q.state = [...f.states];
  if (f.sounds.length) q.sound = [...f.sounds];
  if (f.q) q.q = f.q;
  if (f.crate) q.crate_id = f.crate;
  if (f.near) q.near_song_id = f.near;
  return q;
}
