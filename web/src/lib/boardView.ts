// The Muse Board view model (C100.2, C100.6): the Board payload as a grid, chip selection with the
// 50-take cap, and the filter <-> query string mapping. Pure functions; the page owns the state.
import type { components } from "../api/schema";
import { MAX_TAKES } from "./genIntent";

type S = components["schemas"];
export type Board = S["Board"];
export type Chip = S["SongChip"];
type BoardQuery = NonNullable<import("../api/schema").operations["get_muse_board"]["parameters"]["query"]>;

export const NONE_LABEL = "(none)";
export const UNASSIGNED_LABEL = "Unassigned";

export interface GridColumn {
  angle: string | null;
  label: string;
  songs: number;
}
export interface GridCell {
  angle: string | null;
  chips: Chip[];
}
export interface GridRow {
  key: string;
  muse: S["MuseRef"] | null;
  label: string;
  songsTotal: number;
  counts: S["BoardCounts"];
  /** Aligned with `GridModel.columns`; empty cells have no chips. */
  cells: GridCell[];
  /** The row's chips in cell (column) order: the order shift-click ranges walk. */
  chipIds: string[];
  chipsMore: boolean;
  nextUnassignedCursor: string | null;
}
export interface GridModel {
  columns: GridColumn[];
  rows: GridRow[];
}

/** Server order of the angles, `(none)` last; angles that only appear in cells are added before it. */
function columnsOf(b: Board): GridColumn[] {
  const named: GridColumn[] = [];
  const seen = new Set<string>();
  let none: GridColumn | null = null;
  const add = (angle: string | null, songs: number) => {
    if (angle === null) {
      none ??= { angle: null, label: NONE_LABEL, songs };
      return;
    }
    if (seen.has(angle)) return;
    seen.add(angle);
    named.push({ angle, label: angle, songs });
  };
  for (const c of b.columns) add(c.angle, c.songs);
  for (const r of b.rows) for (const c of r.cells) if (c.chips.length > 0) add(c.angle, 0);
  return none ? [...named, none] : named;
}

export function buildGrid(b: Board): GridModel {
  const columns = columnsOf(b);
  const index = new Map<string | null, number>(columns.map((c, i) => [c.angle, i]));
  const rows: GridRow[] = b.rows.map((r) => {
    const cells: GridCell[] = columns.map((c) => ({ angle: c.angle, chips: [] }));
    for (const c of r.cells) {
      const i = index.get(c.angle);
      if (i !== undefined) cells[i]!.chips.push(...c.chips);
    }
    const chipIds: string[] = [];
    for (const c of cells) for (const ch of c.chips) chipIds.push(ch.id);
    return {
      key: r.muse ? r.muse.id : "unassigned",
      muse: r.muse ?? null,
      label: r.muse ? r.muse.title : UNASSIGNED_LABEL,
      songsTotal: r.songs_total,
      counts: r.counts,
      cells,
      chipIds,
      chipsMore: r.chips_more,
      nextUnassignedCursor: r.next_unassigned_cursor,
    };
  });
  // Unassigned is always the final row, whatever order the pages arrived in.
  rows.sort((a, b2) => Number(a.muse === null) - Number(b2.muse === null));
  return { columns, rows };
}

/** Appending a page: rows of the same Muse merge in the caller; here the Board of the next page is simply concatenated. */
export function mergeBoards(a: Board, b: Board): Board {
  const rows = [...a.rows];
  for (const r of b.rows) {
    const i = rows.findIndex((x) => (x.muse?.id ?? null) === (r.muse?.id ?? null));
    if (i < 0) rows.push(r);
    else {
      const prev = rows[i]!;
      const cells = prev.cells.map((c) => ({ ...c, chips: [...c.chips] }));
      for (const c of r.cells) {
        const hit = cells.find((x) => x.angle === c.angle);
        if (hit) hit.chips.push(...c.chips.filter((ch) => !hit.chips.some((o) => o.id === ch.id)));
        else cells.push({ ...c, chips: [...c.chips] });
      }
      rows[i] = { ...r, cells };
    }
  }
  return { ...b, columns: b.columns, rows };
}

// ---- selection ----

export interface Selection {
  ids: ReadonlySet<string>;
  /** The last toggled chip: the start of a shift-range. */
  anchor: string | null;
}
export function emptySelection(): Selection {
  return { ids: new Set(), anchor: null };
}
export function toggleChip(s: Selection, id: string): Selection {
  const ids = new Set(s.ids);
  if (ids.has(id)) ids.delete(id);
  else ids.add(id);
  return { ids, anchor: id };
}
/** Shift-click: select every chip of `row` between the anchor and `id`; with no anchor in this row it is a plain toggle. */
export function rangeSelect(s: Selection, row: GridRow, id: string): Selection {
  const to = row.chipIds.indexOf(id);
  const from = s.anchor === null ? -1 : row.chipIds.indexOf(s.anchor);
  if (to < 0 || from < 0) return toggleChip(s, id);
  const ids = new Set(s.ids);
  for (let i = Math.min(from, to); i <= Math.max(from, to); i++) ids.add(row.chipIds[i]!);
  return { ids, anchor: id };
}

function chipsInOrder(g: GridModel): Chip[] {
  const out: Chip[] = [];
  for (const r of g.rows) for (const c of r.cells) out.push(...c.chips);
  return out;
}

/** "Select all drafts in view": at most MAX_TAKES, keeping what is already selected. */
export function selectAllDrafts(g: GridModel, s: Selection): { selection: Selection; note: string } {
  const ids = new Set(s.ids);
  let capped = false;
  for (const ch of chipsInOrder(g)) {
    if (ch.state !== "draft" || ids.has(ch.id)) continue;
    if (ids.size >= MAX_TAKES) {
      capped = true;
      break;
    }
    ids.add(ch.id);
  }
  return { selection: { ids, anchor: s.anchor }, note: capped ? `Selected the first ${MAX_TAKES} drafts: generation is limited to ${MAX_TAKES} per batch.` : "" };
}

/** The draft ids Estimate will use (grid order), and how many selected chips were skipped for not being drafts. */
export function draftsForEstimate(g: GridModel, s: Selection): { draftIds: string[]; skipped: number; note: string } {
  const draftIds: string[] = [];
  let skipped = 0;
  const present = new Set<string>();
  for (const ch of chipsInOrder(g)) {
    if (!s.ids.has(ch.id) || present.has(ch.id)) continue;
    present.add(ch.id);
    if (ch.state === "draft" && ch.draft_id) draftIds.push(ch.draft_id);
    else skipped++;
  }
  const note = skipped > 0 ? `${skipped} selected ${skipped === 1 ? "song is" : "songs are"} not drafts and will be skipped.` : "";
  return { draftIds, skipped, note };
}

// ---- filters ----

export type SongState = "draft" | "rendered" | "judged" | "picked";
export const SONG_STATES: readonly SongState[] = ["draft", "rendered", "judged", "picked"];

export interface BoardFilters {
  session: string;
  angle: string;
  states: SongState[];
  /** `facet:value` selections. */
  sounds: string[];
  q: string;
  muse: string;
}

export function filtersFromSearch(search: string): BoardFilters {
  const p = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const states = [...new Set(p.getAll("state"))].filter((x): x is SongState => (SONG_STATES as readonly string[]).includes(x));
  return {
    session: p.get("session") ?? "",
    angle: p.get("angle") ?? "",
    states,
    sounds: [...new Set(p.getAll("sound"))].filter(Boolean),
    q: p.get("q") ?? "",
    muse: p.get("muse") ?? "",
  };
}

/** The query string (no leading `?`) for the page URL. */
export function filtersToSearch(f: BoardFilters): string {
  const p = new URLSearchParams();
  if (f.session) p.set("session", f.session);
  if (f.angle) p.set("angle", f.angle);
  for (const s of f.states) p.append("state", s);
  for (const s of f.sounds) p.append("sound", s);
  if (f.q) p.set("q", f.q);
  if (f.muse) p.set("muse", f.muse);
  return p.toString();
}

/** The get_muse_board query for these filters. */
export function filtersToQuery(f: BoardFilters): BoardQuery {
  const q: BoardQuery = { detail: "chips" };
  if (f.session) q.session_id = f.session;
  if (f.angle) q.angle = f.angle;
  if (f.states.length) q.state = [...f.states];
  if (f.sounds.length) q.sound = [...f.sounds];
  if (f.q) q.q = f.q;
  if (f.muse) q.muse_id = f.muse;
  return q;
}

export function isFiltered(f: BoardFilters): boolean {
  return filtersToSearch(f) !== "";
}
