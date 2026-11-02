// View helpers for the Song feature strip (C145.6): basis words, the key and energy badge texts, the subject
// note, and the deep links into the Atlas. Pure functions; the components own the markup.
import type { components } from "../api/schema";
import { filterFromSearch, filterToSearch, type AtlasFilter } from "./atlasView";

type S = components["schemas"];
export type Features = NonNullable<S["SongView"]["features"]>;
export type KeyInfo = Pick<S["FeaturesKey"], "camelot" | "name" | "basis">;
export type EnergyInfo = Pick<S["FeaturesEnergy"], "value" | "basis">;
export type NextActionItem = S["NextAction"];

const BASIS_WORDS: Record<string, string> = {
  measured: "measured",
  declared: "declared",
  plan: "from the plan",
  predicted: "predicted",
};

/** The word for a basis (`measured`, `declared`, `plan`, `predicted`); unknown bases pass through, none gives "". */
export function basisWord(basis: string | null | undefined): string {
  if (!basis) return "";
  return BASIS_WORDS[basis] ?? basis;
}

/** Predicted values are guesses: drawn hollow, like the Atlas draws them. Everything else is solid. */
export function isHollow(basis: string | null | undefined): boolean {
  return basis === "predicted";
}

/** `8A · A minor · measured`; null without a key (the badge is then absent). */
export function keyText(k: KeyInfo | null | undefined): string | null {
  if (!k?.camelot) return null;
  return [k.camelot, k.name, basisWord(k.basis)].filter(Boolean).join(" · ");
}

/** `0.53 measured`; null without a value. */
export function energyText(e: EnergyInfo | null | undefined): string | null {
  if (!e || e.value === null || e.value === undefined) return null;
  return [e.value.toFixed(2), basisWord(e.basis)].filter(Boolean).join(" ");
}

/** The tempo chip for the features block: `75 BPM · measured`, with the octave rule named. */
export function tempoFeatureText(t: Features["tempo"] | null | undefined): string | null {
  if (!t || t.bpm === null || t.bpm === undefined) return null;
  const bpm = Math.round(t.bpm * 10) / 10;
  return [`${bpm} BPM`, basisWord(t.basis), t.octave ? "octave-corrected" : ""].filter(Boolean).join(" · ");
}

/** The explanation of a Song whose values come from its draft because every take was discarded. */
export function subjectNoteText(note: string | null | undefined): string {
  if (note === "all_takes_discarded") return "Every take is discarded, so these values come from the draft.";
  return "";
}

/** Whether the strip has anything to show beyond the pending marker. */
export function hasFeatureValues(f: Features | null | undefined): boolean {
  return !!f && (keyText(f.key) !== null || energyText(f.energy) !== null || tempoFeatureText(f.tempo) !== null);
}

/** `/atlas` deep link for a partial filter (the page reads it back with `filterFromSearch`). */
export function atlasHref(over: Partial<AtlasFilter>): string {
  const qs = filterToSearch({ ...filterFromSearch(""), ...over });
  return qs ? `/atlas?${qs}` : "/atlas";
}

/** "Show on the Atlas" for a Song: the map focused on its neighbourhood. */
export const songAtlasHref = (songId: string): string => atlasHref({ near: songId });
/** "Show on the Atlas" for a Muse: the map filtered to its Songs. */
export const museAtlasHref = (museId: string): string => atlasHref({ muse: museId });

/** At most `max` actions, highest priority first (stable for equal priorities); `more` counts the rest. */
export function shortList(items: readonly NextActionItem[], max = 5): { shown: NextActionItem[]; more: number } {
  const sorted = items.map((it, i) => ({ it, i })).sort((a, b) => b.it.priority - a.it.priority || a.i - b.i).map((x) => x.it);
  return { shown: sorted.slice(0, max), more: Math.max(0, sorted.length - max) };
}
