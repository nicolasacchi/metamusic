// ACE-Step mode panels (E2): pure state-to-intent builders, so the exact ItemIn JSON per mode is unit-tested.
import type { components } from "../api/schema";
import type { EngineCaps } from "./engines";
import { ACESTEP, MAX_COMPLETE_TRACKS, MAX_STEM_TRACKS, parseSeeds, validateIntent, type EngineMode, type GenerationIntent } from "./genIntent";

type Patch = components["schemas"]["Patch"];
type EditIn = components["schemas"]["EditIn"];
export type StemName = NonNullable<EditIn["tracks"]>[number];

/** The stem names of EditIn.tracks. */
export const STEM_NAMES: readonly StemName[] = ["backing_vocals", "bass", "brass", "drums", "fx", "guitar", "keyboard", "percussion", "strings", "synth", "vocals", "woodwinds"];
export const TIME_SIGNATURES = ["2", "3", "4", "6"] as const;
/** The server's longest span in seconds. */
export const MAX_SECONDS = 600;
/** The strength the cover panel preselects (the server has no default). */
export const DEFAULT_STRENGTH = 0.5;
/** Strength presets with what they do. */
export const STRENGTH_HINTS: readonly { value: number; label: string }[] = [
  { value: 0.2, label: "0.2 restyle most" },
  { value: 0.5, label: "0.5 balanced" },
  { value: 1, label: "1.0 stay closest" },
];

/** A strength from text: 0..1 rounded to 0.001; null when not a number in range. */
export function parseStrength(text: string): number | null {
  const t = text.trim();
  if (!/^\d(\.\d*)?$/.test(t)) return null;
  const n = Number(t);
  if (!Number.isFinite(n) || n > 1) return null;
  return Math.round(n * 1000) / 1000;
}

/** Seconds from a text field: rounded half away from zero to 0.1 s (12.34 gives 12.3, 12.35 gives 12.4); null when not a number in 0..600. */
export function parseSeconds(text: string): number | null {
  const t = text.trim();
  if (!/^\d{1,4}(\.\d*)?$/.test(t)) return null;
  const n = Number(t);
  if (!Number.isFinite(n) || n > MAX_SECONDS) return null;
  return Math.round(Number((n * 10).toPrecision(12))) / 10;
}

/** The form state of a mode panel; every field is the raw text so an empty field stays distinct from 0. */
export interface PanelState {
  from: string;
  to: string;
  extend: string;
  tracks: StemName[];
  /** complete only: the extracted stem used as the source; empty is the full mix. */
  sourceStem: StemName | "";
  think: boolean;
  bpm: string;
  keyscale: string;
  timesignature: string;
  duration: string;
  style: string;
  lyrics: string;
  /** cover: the strength as text, 0..1 (1 stays closest to the source, about 0.2 restyles most). */
  strength: string;
  nSeeds: string;
  seedsText: string;
}

/** The panel state for an intent: what it already carries, empty otherwise. */
export function panelFromIntent(i: GenerationIntent): PanelState {
  const n = (v: number | undefined) => (v === undefined ? "" : String(v));
  const p = i.patch ?? {};
  return {
    from: n(i.mode === "reference" ? i.reference?.from_s : i.edit?.from_s),
    to: n(i.mode === "reference" ? i.reference?.to_s : i.edit?.to_s),
    extend: n(i.edit?.extend_s),
    tracks: [...(i.edit?.tracks ?? [])],
    sourceStem: i.edit?.source_stem ?? "",
    think: p.cot === "think",
    bpm: n(p.bpm),
    keyscale: p.keyscale ?? "",
    timesignature: p.timesignature ?? "",
    duration: n(p.duration_s),
    style: p.style ?? "",
    lyrics: p.lyrics ?? "",
    strength: n(i.edit?.strength ?? (i.mode === "cover" ? DEFAULT_STRENGTH : undefined)),
    nSeeds: String(i.n_seeds ?? 1),
    seedsText: i.seeds?.join(", ") ?? "",
  };
}

export type PanelResult = { ok: true; intent: GenerationIntent } | { ok: false; error: string };

const fail = (error: string): PanelResult => ({ ok: false, error });

function limitMessage(what: string, span: number, min: number, max: number): string | null {
  if (min > 0 && span < min) return `${what} must be at least ${min} s (it is ${round1(span)} s).`;
  if (max > 0 && span > max) return `${what} must be at most ${max} s (it is ${round1(span)} s).`;
  return null;
}
const round1 = (n: number) => Math.round(n * 10) / 10;

/** The stem names a take can be remixed from, in the canonical order (its StemLink / StemInfo names). */
export function stemChoices(stems: readonly { name: string }[]): StemName[] {
  const have = new Set(stems.map((s) => s.name));
  return STEM_NAMES.filter((n) => have.has(n));
}

/** The patch of a port / reference panel: only the fields the user filled in. */
function patchOf(st: PanelState): { patch: Patch } | { error: string } {
  const patch: Patch = {};
  if (st.style.trim()) patch.style = st.style.trim();
  if (st.lyrics.trim()) patch.lyrics = st.lyrics;
  if (st.think) patch.cot = "think";
  if (st.bpm.trim()) {
    if (!/^\d{1,3}$/.test(st.bpm.trim()) || Number(st.bpm) < 30 || Number(st.bpm) > 300) return { error: "Tempo must be a whole number from 30 to 300." };
    patch.bpm = Number(st.bpm);
  }
  if (st.keyscale.trim()) patch.keyscale = st.keyscale.trim();
  if (st.timesignature) {
    if (!(TIME_SIGNATURES as readonly string[]).includes(st.timesignature)) return { error: "Pick a time signature from the list." };
    patch.timesignature = st.timesignature as Patch["timesignature"];
  }
  if (st.duration.trim()) {
    const d = parseSeconds(st.duration);
    if (d === null || d <= 0) return { error: "Duration must be a number of seconds up to 600." };
    patch.duration_s = d;
  }
  return { patch };
}

/**
 * The intent a panel produces for `mode`. `base` carries the source (parent_track_id, or the reference take). `caps`
 * (when list_engines answered) adds the engine's limits as early messages; the server stays the authority.
 */
export function buildPanel(mode: EngineMode, base: GenerationIntent, st: PanelState, caps: EngineCaps | null): PanelResult {
  const seeds = parseSeeds(st.seedsText);
  if (seeds === null) return fail("Seeds must be distinct whole numbers.");
  const n = Number(st.nSeeds);
  if (!Number.isInteger(n) || n < 1) return fail("Takes must be a whole number of at least 1.");
  const seedPart = seeds.length ? { seeds } : { n_seeds: n };
  const keep: GenerationIntent = { mode, engine: ACESTEP, ...seedPart };
  if (base.parent_track_id) keep.parent_track_id = base.parent_track_id;
  if (base.title) keep.title = base.title;

  let intent: GenerationIntent;
  switch (mode) {
    case "port":
    case "reference": {
      const p = patchOf(st);
      if ("error" in p) return fail(p.error);
      intent = { ...keep };
      if (Object.keys(p.patch).length) intent.patch = p.patch;
      if (mode === "reference") {
        if (!base.reference?.track_id) return fail("Pick the take to use as the reference.");
        const a = st.from.trim() === "" ? undefined : parseSeconds(st.from);
        const b = st.to.trim() === "" ? undefined : parseSeconds(st.to);
        if (a === null || b === null) return fail("Window times must be seconds from 0 to 600.");
        if ((a === undefined) !== (b === undefined)) return fail("Give both ends of the window, or neither for the default.");
        if (a !== undefined && b !== undefined) {
          if (!(a < b)) return fail("The window must end after it starts.");
          const msg = caps ? limitMessage("The reference window", b - a, 0, caps.reference_max_s) : null;
          if (msg) return fail(msg);
        }
        intent.reference = { track_id: base.reference.track_id, ...(a !== undefined && b !== undefined ? { from_s: a, to_s: b } : {}) };
      }
      break;
    }
    case "repaint": {
      const a = parseSeconds(st.from);
      const b = parseSeconds(st.to);
      if (a === null || b === null) return fail("Enter the start and end of the range in seconds.");
      if (!(a < b)) return fail("The range must end after it starts.");
      const msg = caps ? limitMessage("The repainted range", b - a, caps.repaint_min_s, caps.repaint_max_s) : null;
      if (msg) return fail(msg);
      intent = { ...keep, edit: { from_s: a, to_s: b } };
      break;
    }
    case "extend": {
      const e = parseSeconds(st.extend);
      if (e === null || e <= 0) return fail("Enter how many seconds to add.");
      const msg = caps ? limitMessage("The extension", e, caps.extend_min_s, caps.extend_max_s) : null;
      if (msg) return fail(msg);
      intent = { ...keep, edit: { extend_s: e } };
      break;
    }
    case "cover": {
      const style = st.style.trim();
      if (!style) return fail("Describe the new style to restyle with.");
      const s = parseStrength(st.strength);
      if (s === null) return fail("Strength must be a number from 0 to 1.");
      intent = { ...keep, patch: { style, ...(st.lyrics.trim() ? { lyrics: st.lyrics } : {}) }, edit: { strength: s } };
      break;
    }
    case "complete":
    case "stems": {
      const max = mode === "complete" ? MAX_COMPLETE_TRACKS : MAX_STEM_TRACKS;
      if (st.tracks.length === 0) return fail("Pick at least one track name.");
      if (st.tracks.length > max) return fail(`Pick at most ${max} track names.`);
      intent = { ...keep, edit: { tracks: [...st.tracks] } };
      if (mode === "complete" && st.sourceStem) {
        // remix: the stem of the take replaces its full mix as the source; the style is the new arrangement's
        intent.edit = { tracks: [...st.tracks], source_stem: st.sourceStem };
        if (st.style.trim()) intent.patch = { style: st.style.trim() };
      }
      break;
    }
  }
  const bad = validateIntent(intent);
  return bad ? fail(bad) : { ok: true, intent };
}

/** What the mode does, for the panel heading. */
export const MODE_TITLES: Record<EngineMode, string> = {
  port: "Port to ACE-Step",
  extend: "Extend",
  repaint: "Repaint a section",
  reference: "Render with a reference",
  complete: "Complete with tracks",
  stems: "Extract stems",
  cover: "Restyle the whole take",
};
