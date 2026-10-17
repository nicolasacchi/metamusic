// Judge one by one (wave 4b): the pure model behind the card UI. A source list of takes, a cursor over it, one undo slot,
// and the blind-mode scrubbing of identity. Nothing here touches the DOM, the network or the player.
import { songLine, type SongRef } from "./songLine";

/** A rating moves the card on after this long (a second tap on a star inside the window re-rates the same take). */
export const ADVANCE_MS = 700;
/** The Undo toast stays this long. */
export const UNDO_MS = 5000;

export interface JudgeTake extends SongRef {
  id: string;
  title: string;
  duration_s: number;
  stars: number | null;
  status: string;
  song_id?: string | null;
  is_pick?: boolean;
}

/** What Undo restores: the rating the take had before the one just given, and the card position it was given at. */
export interface UndoEntry {
  id: string;
  prev: number | null;
  index: number;
}

export interface QueueState<T extends JudgeTake = JudgeTake> {
  takes: readonly T[];
  /** takes.length once the list is used up. */
  index: number;
  undo: UndoEntry | null;
  rated: number;
}

export function startQueue<T extends JudgeTake>(takes: readonly T[]): QueueState<T> {
  return { takes: dedupe(takes), index: 0, undo: null, rated: 0 };
}

function dedupe<T extends JudgeTake>(takes: readonly T[]): T[] {
  const seen = new Set<string>();
  const out: T[] = [];
  for (const t of takes) {
    if (seen.has(t.id)) continue;
    seen.add(t.id);
    out.push(t);
  }
  return out;
}

export function currentTake<T extends JudgeTake>(s: QueueState<T>): T | null {
  return s.takes[s.index] ?? null;
}

export function isDone(s: QueueState): boolean {
  return s.index >= s.takes.length;
}

/** More takes from the next page; takes already in the list are ignored. */
export function append<T extends JudgeTake>(s: QueueState<T>, more: readonly T[]): QueueState<T> {
  const have = new Set(s.takes.map((t) => t.id));
  const add = dedupe(more).filter((t) => !have.has(t.id));
  return add.length === 0 ? s : { ...s, takes: [...s.takes, ...add] };
}

/** Skip: the card moves on, nothing is rated, and the undo slot keeps the last rating (it still applies). */
export function skip<T extends JudgeTake>(s: QueueState<T>): QueueState<T> {
  return { ...s, index: Math.min(s.takes.length, s.index + 1) };
}

/** The card follows the player to a take of the list (the track ended and the next one started). */
export function moveTo<T extends JudgeTake>(s: QueueState<T>, id: string): QueueState<T> {
  const i = s.takes.findIndex((t) => t.id === id);
  return i < 0 || i === s.index ? s : { ...s, index: i };
}

/** A rating was given at `entry.index` and the card advances. */
export function advanceAfterRating<T extends JudgeTake>(s: QueueState<T>, entry: UndoEntry): QueueState<T> {
  return { ...s, index: Math.min(s.takes.length, entry.index + 1), undo: entry, rated: s.rated + 1 };
}

/** Undo: back to the rated take, the undo slot empties. */
export function undoRating<T extends JudgeTake>(s: QueueState<T>): { state: QueueState<T>; entry: UndoEntry | null } {
  const e = s.undo;
  if (!e) return { state: s, entry: null };
  return { state: { ...s, index: e.index, undo: null, rated: Math.max(0, s.rated - 1) }, entry: e };
}

/** "2 of 7"; `more` marks a source with further pages. */
export function positionLabel(s: QueueState, more = false): string {
  if (s.takes.length === 0) return "";
  return `${Math.min(s.index + 1, s.takes.length)} of ${s.takes.length}${more ? "+" : ""}`;
}

/** The takes after the current one need to be loaded (3 ahead is enough for the player queue to stay full). */
export function needsMore(s: QueueState, hasMore: boolean): boolean {
  return hasMore && s.takes.length - s.index <= 3;
}

// ---- blind mode

/** Take A, Take B, ... Take Z, Take AA ... by position in the list. */
export function takeLabel(i: number): string {
  let n = Math.max(0, Math.floor(i));
  let s = "";
  do {
    s = String.fromCharCode(65 + (n % 26)) + s;
    n = Math.floor(n / 26) - 1;
  } while (n >= 0);
  return `Take ${s}`;
}

export interface CardView {
  title: string;
  /** "Song · angle", or "" */
  songLine: string;
  cover: boolean;
  waveform: boolean;
  openTake: boolean;
}

/** What the card may show. In blind mode nothing but the label survives: no title, Song, angle, cover or waveform. */
export function cardView(t: JudgeTake, index: number, blind: boolean): CardView {
  if (blind) return { title: takeLabel(index), songLine: "", cover: false, waveform: false, openTake: false };
  return { title: t.title, songLine: songLine(t), cover: true, waveform: true, openTake: true };
}

/** The Media Session label for the playing track: null (show the real metadata) unless it is a take of a blind card. */
export function mediaMask(blind: boolean, ids: readonly string[], trackId: string): string | null {
  if (!blind) return null;
  const i = ids.indexOf(trackId);
  return i < 0 ? null : takeLabel(i);
}

/** The toast text: never a title (blind mode keeps identity out of the DOM). */
export function ratedMessage(stars: number | null): string {
  return stars === null ? "Rating cleared" : `Rated ${stars} ${stars === 1 ? "star" : "stars"}`;
}

// ---- chorus

export interface TimedSection {
  kind: string;
  label?: string;
  start_s: number | null;
}

/** Start of the first chorus with a known time; null when the take has none (the button is then hidden). */
export function chorusStart(sections: readonly TimedSection[]): number | null {
  for (const s of sections) {
    if (s.start_s === null || !Number.isFinite(s.start_s)) continue;
    if (/chorus/i.test(s.kind) || /chorus/i.test(s.label ?? "")) return s.start_s;
  }
  return null;
}

// ---- source

export type JudgeSource = { kind: "unrated" } | { kind: "new" } | { kind: "song"; id: string } | { kind: "muse"; id: string };

/** The page's source from its query string: ?from=new, ?song=<id>, ?muse=<id>; anything else is the unrated default. */
export function sourceFromSearch(search: string): JudgeSource {
  const q = new URLSearchParams(search);
  const song = q.get("song");
  if (song) return { kind: "song", id: song };
  const muse = q.get("muse");
  if (muse) return { kind: "muse", id: muse };
  if (q.get("from") === "new") return { kind: "new" };
  return { kind: "unrated" };
}

export function sourceHref(src: JudgeSource): string {
  switch (src.kind) {
    case "new":
      return "/judge?from=new";
    case "song":
      return `/judge?song=${encodeURIComponent(src.id)}`;
    case "muse":
      return `/judge?muse=${encodeURIComponent(src.id)}`;
    default:
      return "/judge";
  }
}

export function sourceLabel(src: JudgeSource, name = ""): string {
  switch (src.kind) {
    case "new":
      return "Judge new";
    case "song":
      return name ? `Judge ${name}` : "Judge Song";
    case "muse":
      return name ? `Judge ${name}` : "Judge Muse";
    default:
      return "Judge new unrated";
  }
}
