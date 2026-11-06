// The T1 fixture library: which seeded Takes the transform specs use and the request bodies they send. The e2e profile
// (seed-synthetic --profile e2e) is the whole fixture; nothing here creates audio. The renders are real (the exec child over
// this binary), and short because the seeded Takes are 2 to 12 seconds long.
import type { APIRequestContext } from "@playwright/test";
import { allTracks, type Item } from "../helpers";

/** Seeded titles (internal/synth). Song 03 and Song 05 have a recipe, a plan tempo of 120 and are long enough for a 2-bar morph. */
export const T1_TITLES = {
  /** 12 s: the source of every render; its glacier x10 takes about as long as the cancel specs need. */
  source: "Syn song 03",
  /** 9 s: the second input of the morph (no shared lyrics with the source: picked through "Another take"). */
  partner: "Syn song 05",
  /** Two Takes of one Song (equal lyrics, other seeds): the "same lyrics, other styles" partner picker offers exactly this one. */
  twinA: "Syn song 01 s1001",
  twinB: "Syn song 01 s2001",
  /** A reference import: no recipe, so a transform of it is refused (RECIPE_UNKNOWN). */
  reference: "Syn ref a",
} as const;

export interface T1Tracks {
  source: Item;
  partner: Item;
  twinA: Item;
  twinB: Item;
  reference: Item;
}

export async function t1Tracks(request: APIRequestContext): Promise<T1Tracks> {
  const all = await allTracks(request);
  const by = (title: string): Item => {
    const t = all.find((x) => x.title === title);
    if (!t) throw new Error(`seed take ${title} missing`);
    return t;
  };
  return { source: by(T1_TITLES.source), partner: by(T1_TITLES.partner), twinA: by(T1_TITLES.twinA), twinB: by(T1_TITLES.twinB), reference: by(T1_TITLES.reference) };
}

/** A body for create_transform / estimate_transform. Explicit downbeats keep the morph deterministic (no measurement). */
export type TransformBody = Record<string, unknown> & { kind: string };

export const masterBody = (source: string, preset = "neutral"): TransformBody => ({ kind: "master", master: { source_id: source, preset } });
export const widenBody = (source: string, width = 1.3): TransformBody => ({ kind: "widen", widen: { source_id: source, width } });
/** factor 10 of the 12 s Song 03 renders 120 s of audio: about 17 s of CPU, a render that is still running while a spec looks at it. */
export const slowGlacierBody = (source: string, factor = 10): TransformBody => ({ kind: "glacier", glacier: { source_id: source, factor } });
export const morphBody = (a: string, b: string, bars = 2): TransformBody => ({
  kind: "morph",
  morph: { track_a_id: a, track_b_id: b, downbeat_a_s: 0.4, downbeat_b_s: 0.2, bars },
});
