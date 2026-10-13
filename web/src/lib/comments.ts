// Pure comment helpers: timestamps, ordering, waveform pins, section chips.
import { fmt } from "./time";

export interface CommentLike {
  id: string;
  at_second: number | null;
  created_at: string;
  kind: "note" | "fix" | "keep" | "marker";
  section: { label: string; confidence: number } | null;
}

export interface Pin {
  id: string;
  t: number;
  kind: CommentLike["kind"];
  x: number;
}

/** Seconds -> "m:ss" (floored). */
export function formatAt(seconds: number): string {
  return fmt(seconds);
}

export function sortComments<T extends CommentLike>(items: readonly T[], order: "newest" | "time"): T[] {
  const byNewest = (a: T, b: T) => (a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : 0);
  return [...items].sort((a, b) => {
    if (order === "newest") return byNewest(a, b);
    if (a.at_second === null && b.at_second === null) return -byNewest(a, b);
    if (a.at_second === null) return 1;
    if (b.at_second === null) return -1;
    return a.at_second - b.at_second || -byNewest(a, b);
  });
}

/** x offsets (px) of timed comments on a waveform `width` px wide. Empty when the duration is unknown. */
export function pinPositions(items: readonly Pick<CommentLike, "id" | "at_second" | "kind">[], duration: number, width: number): Pin[] {
  if (!(duration > 0) || !(width > 0)) return [];
  const out: Pin[] = [];
  for (const c of items) {
    if (c.at_second === null) continue;
    const t = Math.min(Math.max(0, c.at_second), duration);
    out.push({ id: c.id, t: c.at_second, kind: c.kind, x: (t / duration) * width });
  }
  return out;
}

/** "chorus 1 · ~70%", or null when the comment has no section. */
export function sectionChip(c: Pick<CommentLike, "section">): string | null {
  return c.section ? `${c.section.label} · ~${Math.round(c.section.confidence * 100)}%` : null;
}
