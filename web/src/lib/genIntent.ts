// The generation intent: what the user asked to generate, carried across the full-page navigation to
// /human/generate in sessionStorage (C84.1), and converted to an estimate request.
import type { components } from "../api/schema";

type ItemIn = components["schemas"]["ItemIn"];
type Patch = components["schemas"]["Patch"];
type VariantBody = components["schemas"]["EstimateVariantInBody"];
type EditIn = components["schemas"]["EditIn"];
type RefIn = components["schemas"]["RefIn"];

export const INTENT_KEY = "mm.genIntent";
/** D-M3-7: at most 50 takes per batch. */
export const MAX_TAKES = 50;

export type VariantMode = "new_seed" | "same_seed_edit" | "lyric_edit" | "style_edit";
const VARIANT_MODES: readonly string[] = ["new_seed", "same_seed_edit", "lyric_edit", "style_edit"];

/** The ACE-Step modes (E2): each acts on a source take, or (reference) renders with a take's window as the reference. */
export type EngineMode = "port" | "extend" | "repaint" | "reference" | "complete" | "stems";
export const ENGINE_MODES: readonly EngineMode[] = ["port", "extend", "repaint", "reference", "complete", "stems"];
export type EngineName = "yue2" | "acestep15";
export const ENGINE_NAMES: readonly string[] = ["yue2", "acestep15"];
/** The one engine other than the default; engine modes imply it. */
export const ACESTEP: EngineName = "acestep15";
/** At most this many tracks to complete with (C161). */
export const MAX_COMPLETE_TRACKS = 4;
/** The most stem names one item carries (EditIn.tracks maxItems). */
export const MAX_STEM_TRACKS = 6;

export type IntentMode = VariantMode | EngineMode | "from_draft" | "from_template" | "compose_new" | "song_pr";

export function isEngineMode(m: string): m is EngineMode {
  return (ENGINE_MODES as readonly string[]).includes(m);
}

/** The engine an intent renders on: its own field, ACE-Step for the engine modes, else yue2. */
export function engineOf(i: { mode: string; engine?: string }): EngineName {
  if (i.engine === ACESTEP) return ACESTEP;
  if (i.engine === undefined && isEngineMode(i.mode)) return ACESTEP;
  return "yue2";
}

/** Which fields apply depends on the mode (see ItemIn in the API schema); the rest stay unset. */
export interface GenerationIntent {
  mode: IntentMode;
  /** Variants: the mother. */
  parent_track_id?: string;
  /** from_draft */
  draft_ids?: string[];
  /** from_template */
  parent_track_ids?: string[];
  template_mode?: VariantMode;
  n_seeds?: number;
  seeds?: number[];
  patch?: Patch;
  /** song_pr (M5): the Song PR to render; priced and created like any generation item. */
  patch_id?: string;
  title?: string;
  /** compose_new */
  recipe?: Record<string, unknown>;
  /** E2: the engine; absent means yue2 (or ACE-Step for an engine mode). Sent as the estimate body's engine, never inside the item. */
  engine?: EngineName;
  /** E2 repaint / extend / complete / stems: the time range, the extension or the stem names. */
  edit?: EditIn;
  /** E2 reference: the take window the render follows. */
  reference?: RefIn;
}

export interface IntentStorage {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
  removeItem(k: string): void;
}

export type EstimateCall =
  | { kind: "variant"; trackId: string; body: VariantBody }
  | { kind: "generation"; body: { items: ItemIn[]; engine?: EngineName } };

export function isVariantMode(m: string): m is VariantMode {
  return VARIANT_MODES.includes(m);
}

function takesOf(i: GenerationIntent): number {
  const per = i.seeds?.length || i.n_seeds || 1;
  if (i.mode === "from_draft") return (i.draft_ids?.length ?? 0) * per;
  if (i.mode === "from_template") return (i.parent_track_ids?.length ?? 0) * per;
  return per;
}

/** A human message when the intent cannot be estimated, else null. */
export function validateIntent(i: GenerationIntent): string | null {
  if (i.mode === "from_draft") {
    if (!i.draft_ids?.length) return "Pick at least one draft.";
    if (i.draft_ids.length > MAX_TAKES) return `Too many drafts: generate in batches of at most ${MAX_TAKES}.`;
  }
  if (i.mode === "from_template") {
    if ((i.parent_track_ids?.length ?? 0) < 2) return "A template edit needs at least two tracks.";
    if ((i.parent_track_ids?.length ?? 0) > MAX_TAKES) return `Too many tracks: generate in batches of at most ${MAX_TAKES}.`;
  }
  if (isVariantMode(i.mode) && !i.parent_track_id) return "Pick a track to vary.";
  const engineBad = validateEngineIntent(i);
  if (engineBad) return engineBad;
  const n = takesOf(i);
  if (n > MAX_TAKES) return `That is ${n} takes: generate in batches of at most ${MAX_TAKES} (at most ${MAX_TAKES} takes per batch).`;
  return null;
}

function validateEngineIntent(i: GenerationIntent): string | null {
  if (!isEngineMode(i.mode)) return null;
  if (engineOf(i) !== ACESTEP) return "This action needs the ACE-Step engine.";
  if (i.mode === "reference") {
    if (!i.reference?.track_id) return "Pick the take to use as the reference.";
    const { from_s: a, to_s: b } = i.reference;
    if (a !== undefined && b !== undefined && !(a < b)) return "The reference window must end after it starts.";
    return null;
  }
  if (!i.parent_track_id) return "Pick a take to work on.";
  const e = i.edit;
  if (i.mode === "repaint") {
    if (e?.from_s === undefined || e.to_s === undefined) return "Select the range to repaint.";
    if (!(e.from_s < e.to_s)) return "The range must end after it starts.";
  }
  if (i.mode === "extend" && !(e?.extend_s !== undefined && e.extend_s > 0)) return "Enter how many seconds to add.";
  if (i.mode === "complete" || i.mode === "stems") {
    const t = e?.tracks ?? [];
    if (t.length === 0) return "Pick at least one track name.";
    if (new Set(t).size !== t.length) return "Each track name can appear once.";
    const max = i.mode === "complete" ? MAX_COMPLETE_TRACKS : MAX_STEM_TRACKS;
    if (t.length > max) return `Pick at most ${max} track names.`;
  }
  return null;
}

export function saveIntent(s: IntentStorage, i: GenerationIntent): void {
  s.setItem(INTENT_KEY, JSON.stringify(i));
}

export function clearIntent(s: IntentStorage): void {
  s.removeItem(INTENT_KEY);
}

function isIntent(v: unknown): v is GenerationIntent {
  if (typeof v !== "object" || v === null) return false;
  const o = v as Record<string, unknown>;
  const mode = o.mode;
  if (typeof mode !== "string") return false;
  if (o.engine !== undefined && (typeof o.engine !== "string" || !ENGINE_NAMES.includes(o.engine))) return false;
  if (mode === "reference") return typeof o.reference === "object" && o.reference !== null && typeof (o.reference as Record<string, unknown>).track_id === "string";
  if (isEngineMode(mode)) return typeof o.parent_track_id === "string";
  if (isVariantMode(mode)) return typeof o.parent_track_id === "string";
  if (mode === "from_draft") return Array.isArray(o.draft_ids) && o.draft_ids.every((x) => typeof x === "string");
  if (mode === "from_template") return Array.isArray(o.parent_track_ids) && typeof o.template_mode === "string" && isVariantMode(o.template_mode);
  if (mode === "song_pr") return typeof o.patch_id === "string" && o.patch_id !== "";
  if (mode === "compose_new") return typeof o.recipe === "object" && o.recipe !== null;
  return false;
}

/** The stored intent, or null when absent or malformed. */
export function loadIntent(s: IntentStorage): GenerationIntent | null {
  const raw = s.getItem(INTENT_KEY);
  if (raw === null) return null;
  try {
    const v: unknown = JSON.parse(raw);
    return isIntent(v) ? v : null;
  } catch {
    return null;
  }
}

/** A single variant of one mother goes to estimate_variant; everything else to estimate_generation. */
export function intentToEstimate(i: GenerationIntent): EstimateCall {
  if (isVariantMode(i.mode) && engineOf(i) === "yue2") {
    const body: VariantBody = { mode: i.mode };
    if (i.n_seeds !== undefined) body.n_seeds = i.n_seeds;
    if (i.seeds !== undefined) body.seeds = i.seeds;
    if (i.patch !== undefined) body.patch = i.patch;
    if (i.title !== undefined) body.title = i.title;
    return { kind: "variant", trackId: i.parent_track_id ?? "", body };
  }
  const { engine: _engine, ...item } = i;
  void _engine;
  const engine = engineOf(i);
  return { kind: "generation", body: { items: [{ ...item, mode: i.mode } as ItemIn], ...(engine === "yue2" ? {} : { engine }) } };
}

/** "1, 2 3" ⇒ [1,2,3]; null when any token is not a safe non-negative integer or one repeats; [] for blank. */
export function parseSeeds(text: string): number[] | null {
  const t = text.trim();
  if (t === "") return [];
  const out: number[] = [];
  for (const p of t.split(/[\s,]+/)) {
    if (!/^\d{1,16}$/.test(p)) return null;
    const n = Number(p);
    if (!Number.isSafeInteger(n) || out.includes(n)) return null;
    out.push(n);
  }
  return out;
}

/** The take (variant) intent for the Track page menu: "New seed" asks for three seeds; edits open the form first. */
export function variantIntent(parentTrackId: string, mode: VariantMode): GenerationIntent {
  return mode === "new_seed" ? { mode, parent_track_id: parentTrackId, n_seeds: 3 } : { mode, parent_track_id: parentTrackId };
}

/** Backlog selection (C84.2). Validation (1..50) is validateIntent's job. */
export function draftsIntent(draftIds: string[]): GenerationIntent {
  return { mode: "from_draft", draft_ids: [...draftIds] };
}

/** Library selection (C84.2): 2..50 known lossless mothers. */
export function templateIntent(parentTrackIds: string[], templateMode: VariantMode): GenerationIntent {
  return { mode: "from_template", parent_track_ids: [...parentTrackIds], template_mode: templateMode };
}

/** "New song": the form on /human/generate fills in the recipe. */
export function composeIntent(): GenerationIntent {
  return { mode: "compose_new", recipe: {} };
}

/** The intent on another engine: yue2 drops the field (the default), anything else sets it. */
export function withEngine(i: GenerationIntent, engine: EngineName): GenerationIntent {
  const { engine: _old, ...rest } = i;
  void _old;
  return engine === "yue2" ? rest : { ...rest, engine };
}

/** Modes whose engine can be chosen on the generate page: variants of a take and the engine's own modes. */
export function engineSwitchable(i: GenerationIntent): boolean {
  return isVariantMode(i.mode) || isEngineMode(i.mode);
}

/** An ACE-Step mode on a source take (the take page, E2). */
export function engineIntent(mode: Exclude<EngineMode, "reference">, parentTrackId: string): GenerationIntent {
  return { mode, engine: ACESTEP, parent_track_id: parentTrackId };
}

/** "Render with this as reference": the window defaults to the server's (the last 20 s) when unset. */
export function referenceIntent(trackId: string, from_s?: number, to_s?: number): GenerationIntent {
  return { mode: "reference", engine: ACESTEP, reference: { track_id: trackId, ...(from_s !== undefined ? { from_s } : {}), ...(to_s !== undefined ? { to_s } : {}) } };
}

/** The slice of whoami the generation entry points need. */
export interface Viewer {
  kind: "human" | "agent" | "device";
  scopes: readonly string[];
}

/** Devices, principals without the generate scope, reference (recipe unknown) and lossy tracks never see generation buttons. */
export function canGenerate(me: Viewer | null, t: { recipe_known: boolean; lossy: boolean }): boolean {
  if (!me || me.kind === "device" || !me.scopes.includes("generate")) return false;
  return t.recipe_known && !t.lossy;
}

/** Who may start any generation from a list page (no per-track check). */
export function mayGenerate(me: Viewer | null): boolean {
  return me !== null && me.kind !== "device" && me.scopes.includes("generate");
}

/** Hand the intent to the full-page /human/generate flow (C84.1). */
export function startGeneration(storage: IntentStorage, i: GenerationIntent, go: (to: string) => void): void {
  saveIntent(storage, i);
  go("/human/generate");
}

/** Reference (recipe unknown) and lossy tracks cannot be mothers: not selectable for a template edit (D-M3-34). */
export function templateSelectable(t: { recipe_known: boolean; lossy: boolean }): boolean {
  return t.recipe_known && !t.lossy;
}

/** Toggle an id in a selection, returning a new set. */
export function toggled(sel: ReadonlySet<string>, id: string, on: boolean): Set<string> {
  const next = new Set(sel);
  if (on) next.add(id);
  else next.delete(id);
  return next;
}
