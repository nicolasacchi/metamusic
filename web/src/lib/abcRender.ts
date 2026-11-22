// Notation for the Desk pages (DESK-W1, m5b-desk C363.1, D-DESK-17): abcjs draws the score; it never plays it. This is the
// only module that imports abcjs, and only through a dynamic import, so the library is a separate chunk that loads on the
// first Desk page. Playback is always the server's audition (DESK-W2), never abcjs's synthesizer.
import type { PlanBarSpan } from "./deskApi";

/** The slice of abcjs this module uses; the tests pass a fake. */
export interface AbcLib {
  renderAbc(target: HTMLElement, abc: string, params: Record<string, unknown>): unknown;
}
export type AbcLoader = () => Promise<AbcLib>;

let cached: Promise<AbcLib> | null = null;
/** Loads abcjs once. A failed load is not cached, so a later page can retry. */
export const loadAbcjs: AbcLoader = () => {
  cached ??= import("abcjs")
    .then((m) => ((m as unknown as { default?: AbcLib }).default ?? (m as unknown as AbcLib)))
    .catch((e: unknown) => {
      cached = null;
      throw e;
    });
  return cached;
};

/** A bar as the page names it: the voice id and the 0-based bar index (the server's numbering). */
export interface BarRef {
  voice: string;
  bar: number;
}

/**
 * The server's spans are byte offsets into the score text; abcjs reports UTF-16 positions (`startChar`). They are equal for
 * ASCII, and differ once a lyric or title carries an accent; this converts the spans so a click lands on the right bar.
 */
export function spansInChars(abc: string, spans: readonly PlanBarSpan[]): PlanBarSpan[] {
  const enc = new TextEncoder();
  if (enc.encode(abc).length === abc.length) return spans.map((s) => ({ ...s }));
  // byte offset -> UTF-16 index, for every offset at a code point boundary
  const at = new Map<number, number>();
  let bytes = 0;
  let units = 0;
  for (const ch of abc) {
    at.set(bytes, units);
    bytes += enc.encode(ch).length;
    units += ch.length;
  }
  at.set(bytes, units);
  const conv = (b: number): number => {
    for (let o = b; o >= 0; o--) {
      const u = at.get(o);
      if (u !== undefined) return u;
    }
    return 0;
  };
  return spans.map((s) => ({ ...s, start: conv(s.start), end: conv(s.end) }));
}

/** The bar whose text holds the character position, or null (a header, a lyric line, a bar line between bars). */
export function barAtChar(spans: readonly PlanBarSpan[], pos: number): BarRef | null {
  for (const s of spans) if (pos >= s.start && pos < s.end) return { voice: s.voice, bar: s.bar };
  return null;
}

export interface RenderOptions {
  /** The text abcjs draws: the stored score, or the display rewrite for a YuE2 score that reads differently. */
  abc: string;
  /** Spans of that same text, in the server's byte offsets. */
  spans: readonly PlanBarSpan[];
  onBar?: (bar: BarRef) => void;
  load?: AbcLoader;
}

/** Draws the score into `target` and routes note clicks to `onBar`. Resolves once drawn; rejects when abcjs cannot load. */
export async function renderScore(target: HTMLElement, o: RenderOptions): Promise<void> {
  const lib = await (o.load ?? loadAbcjs)();
  const spans = spansInChars(o.abc, o.spans);
  lib.renderAbc(target, o.abc, {
    add_classes: true,
    responsive: "resize",
    staffwidth: 740,
    clickListener: (el: { startChar?: number }) => {
      if (typeof el.startChar !== "number") return;
      const hit = barAtChar(spans, el.startChar);
      if (hit) o.onBar?.(hit);
    },
  });
}

/**
 * Marks the selected bar in the drawn notation. With `add_classes` abcjs gives every note a class `abcjs-v<voice index>` and
 * `abcjs-mm<measure number>`; `voices` is the voice ids in score order, so the voice id maps to its index.
 */
export function markBar(target: ParentNode, voices: readonly string[], sel: BarRef | null): void {
  for (const el of Array.from(target.querySelectorAll(".mm-bar-selected"))) el.classList.remove("mm-bar-selected");
  if (!sel) return;
  const v = voices.indexOf(sel.voice);
  if (v < 0) return;
  for (const el of Array.from(target.querySelectorAll(`.abcjs-v${v}.abcjs-mm${sel.bar}`))) el.classList.add("mm-bar-selected");
}
