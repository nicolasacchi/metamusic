// The karaoke view's model (M5b C347.3): which line and word the playhead is on, how a line is classed and titled, the human
// marks and the paging. Pure; KaraokeLyrics.svelte renders it.
import type { components } from "../api/schema";

type S = components["schemas"];
export type Line = S["DictionLine"];
export type Word = S["DictionWord"];
export type Page = S["DictionLines"];
export type MarkValue = "clear" | "unclear" | "none";
export type MarkIn = S["MarkIn"];

/** The key of a line within a take. */
export const lineKey = (l: Pick<Line, "section_idx" | "line_idx">): string => `${l.section_idx}:${l.line_idx}`;

/**
 * Index (in `lines`) of the line the playhead is in: the last line that has started, if its end (when it has one) is still ahead.
 * Lines without a time are skipped. The list is in song order, so start times only grow: a binary search over the timed lines.
 */
export function activeLine(lines: readonly Line[], t: number | null): number {
  if (t === null || !Number.isFinite(t)) return -1;
  const timed: number[] = [];
  for (let i = 0; i < lines.length; i++) if (lines[i]!.start_s !== null) timed.push(i);
  let lo = 0;
  let hi = timed.length - 1;
  let found = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if ((lines[timed[mid]!]!.start_s ?? 0) <= t) {
      found = mid;
      lo = mid + 1;
    } else hi = mid - 1;
  }
  if (found < 0) return -1;
  const idx = timed[found]!;
  const end = lines[idx]!.end_s;
  return end !== null && t >= end ? -1 : idx;
}

/** Index (in `line.words`) of the word being sung: the last word that has started. An estimated line has no word highlight. */
export function activeWord(line: Line, t: number | null): number {
  if (t === null || line.timing !== "heard" || !line.words) return -1;
  let found = -1;
  for (let i = 0; i < line.words.length; i++) {
    const s = line.words[i]!.start_s;
    if (s !== null && s <= t) found = i;
  }
  return found;
}

/** What the person marked on this line ("none" when nothing, or withdrawn). */
export function humanMark(l: Line): MarkValue {
  return l.mark.human ?? "none";
}

/** The mark a press on `wanted` writes: pressing the mark a line already has withdraws it. */
export function toggleMark(l: Line, wanted: "clear" | "unclear"): MarkValue {
  return humanMark(l) === wanted ? "none" : wanted;
}

export function markBody(l: Line, mark: MarkValue): MarkIn {
  return { section_idx: l.section_idx, line_idx: l.line_idx, mark };
}

export function cerText(cer: number | null): string {
  return cer === null ? "not measured" : `${Math.round(cer * 100)} % off`;
}

/** The tooltip of a line: its class and who decided it, the measured error, and a note when its time is only estimated. */
export function lineTitle(l: Line): string {
  const who = l.class_source === "human" ? "you marked it" : "recogniser";
  return `${l.class} (${who}) · ${cerText(l.cer)}${l.timing === "estimated" ? " · time estimated" : ""}`;
}

/** The tooltip of a word; "" for a word heard exactly with a heard time. */
export function wordTitle(w: Word): string {
  if (w.match === "dropped") return "not heard";
  if (w.match === "miss") return `heard as ${w.heard}`;
  if (w.match === "close") return `close: heard ${w.heard}`;
  return w.timing === "interpolated" ? "time interpolated" : "";
}

export interface SectionGroup {
  section_idx: number;
  /** Index in the line list of the section's first line. */
  first: number;
  lines: Line[];
}

/** Consecutive lines of one section. */
export function sectionsOf(lines: readonly Line[]): SectionGroup[] {
  const out: SectionGroup[] = [];
  lines.forEach((l, i) => {
    const last = out[out.length - 1];
    if (last && last.section_idx === l.section_idx) last.lines.push(l);
    else out.push({ section_idx: l.section_idx, first: i, lines: [l] });
  });
  return out;
}

/** A later page appended to an earlier one. A line that comes twice is replaced; the lexicon hits and thresholds are the first page's. */
export function mergePages(a: Page, b: Page): Page {
  const at = new Map(a.lines.map((l, i) => [lineKey(l), i]));
  const lines = [...a.lines];
  for (const l of b.lines) {
    const i = at.get(lineKey(l));
    if (i === undefined) lines.push(l);
    else lines[i] = l;
  }
  return { ...a, lines, truncated: b.truncated, next_cursor: b.next_cursor };
}

/** The server marks empty lists required, but a Go nil slice arrives as null: every list of a page is made a list. */
export function normalizePage(p: Page): Page {
  return {
    ...p,
    lines: (p.lines ?? []).map((l) => ({ ...l, words: l.words ?? [] })),
    lexicon_hits: (p.lexicon_hits ?? []).map((h) => ({ ...h, typical_heard: h.typical_heard ?? [] })),
  };
}
