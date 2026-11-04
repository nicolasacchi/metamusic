// Pure form model of the Transform panel (T1 C214.2): the fields of a kind are generated from the capabilities'
// parameter table, the form state is plain strings, and `buildRequest` turns it into the create/estimate body
// (blank optional scalars go out as null = default) or a per-field error. No network, no Svelte.
import type { components } from "../api/schema";

type S = components["schemas"];
export type KindCaps = S["TransformKindCaps"];
export type ParamCap = S["TransformParamCap"];
export type TransformRequest = S["TransformRequest"];

export type FieldType = "number" | "integer" | "select" | "boolean" | "downbeat" | "bar" | "dryDb" | "text";
export interface Field {
  /** The key in `values` (the parameter name, or `<name>_bar` for a position). */
  key: string;
  /** The parameter name in the request. */
  param: string;
  type: FieldType;
  label: string;
  required: boolean;
  min?: number;
  max?: number;
  step?: number;
  options?: string[];
  /** The default as text, for a placeholder; "" when none is published. */
  placeholder: string;
}

export interface FormState {
  /** Informational: the request names the kind of the capabilities entry it is built from. */
  kind: string;
  title: string;
  /** Raw strings keyed by `Field.key`. A blank string means "use the default". */
  values: Record<string, string>;
}

export interface FormContext {
  /** The Take whose page the panel is on (source, or A of a pair). */
  trackId: string;
  /** Score-render only: the Take carries score.abc. */
  hasScore?: boolean;
}

/** Parameters the panel fills itself (pickers), never as generic fields. */
const ID_PARAMS = new Set(["source_id", "track_a_id", "track_b_id"]);
const AT_PARAMS = new Set(["a_at", "b_at"]);
const DOWNBEAT_PARAMS = new Set(["downbeat_a_s", "downbeat_b_s"]);
/** The whole-score source of score_render is fixed to this Take (from_track_id). */
const SKIPPED = new Set(["score"]);

const SUFFIX: [RegExp, string][] = [
  [/_s$/, " (s)"],
  [/_db$/, " (dB)"],
  [/_dbtp$/, " (dBTP)"],
  [/_hz$/, " (Hz)"],
  [/_deg$/, " (°)"],
  [/_lufs$/, " (LUFS)"],
  [/_ms$/, " (ms)"],
];
/** `fade_in_s` -> `Fade in (s)`. */
export function labelFor(name: string): string {
  let unit = "";
  let base = name;
  for (const [re, u] of SUFFIX) {
    if (re.test(base)) {
      unit = u;
      base = base.replace(re, "");
      break;
    }
  }
  const words = base.replaceAll("_", " ").replace(/\bbpm\b/g, "BPM").replace(/\blufs\b/g, "LUFS").replace(/\bdbtp\b/g, "dBTP");
  return words.charAt(0).toUpperCase() + words.slice(1) + unit;
}

const text = (v: unknown): string => (v === null || v === undefined || typeof v === "object" ? "" : String(v));
const num = (v: number | null | undefined): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);

function stepOf(p: ParamCap): number | undefined {
  if (p.type === "integer") return 1;
  const lo = num(p.min), hi = num(p.max);
  if (lo !== undefined && hi !== undefined) return hi - lo <= 2 ? 0.05 : hi - lo <= 60 ? 0.1 : 1;
  return undefined;
}

/** The fields of a kind, in the table's order, from `get_transform_capabilities`. */
export function fieldsFor(caps: Pick<KindCaps, "kind" | "params">, ctx?: FormContext): Field[] {
  const out: Field[] = [];
  for (const p of caps.params) {
    if (SKIPPED.has(p.name)) continue;
    if (ID_PARAMS.has(p.name)) {
      if (p.name === "track_b_id") out.push({ key: p.name, param: p.name, type: "text", label: "Partner take", required: true, placeholder: "" });
      continue;
    }
    if (AT_PARAMS.has(p.name)) {
      const side = p.name === "a_at" ? "A" : "B";
      out.push({ key: `${p.name}_bar`, param: p.name, type: "bar", label: `${side} starts at bar`, required: false, min: 0, step: 1, placeholder: "auto" });
      continue;
    }
    if (DOWNBEAT_PARAMS.has(p.name)) {
      out.push({ key: p.name, param: p.name, type: "downbeat", label: `Downbeat ${p.name === "downbeat_a_s" ? "A" : "B"} (s)`, required: p.required, min: 0, step: 0.01, placeholder: "" });
      continue;
    }
    if (p.name === "dry_db") {
      out.push({ key: p.name, param: p.name, type: "dryDb", label: "Dry (dB)", required: false, min: num(p.min), max: num(p.max), step: 0.5, placeholder: text(p.default) });
      continue;
    }
    const base = { key: p.name, param: p.name, label: labelFor(p.name), required: p.required, placeholder: text(p.default) };
    if (p.enum && p.enum.length > 0) out.push({ ...base, type: "select", options: p.enum });
    else if (p.type === "boolean") out.push({ ...base, type: "boolean" });
    else if (p.type === "integer" || p.type === "number") out.push({ ...base, type: p.type, min: num(p.min), max: num(p.max), step: stepOf(p) });
    else out.push({ ...base, type: "text" });
  }
  void ctx;
  return out;
}

export function defaultForm(kind: string): FormState {
  return { kind, title: "", values: {} };
}

export type FieldErrors = Record<string, string>;
export interface Built {
  body: TransformRequest | null;
  errors: FieldErrors;
}

function range(f: Field): string {
  if (f.min !== undefined && f.max !== undefined) return `${f.min}–${f.max}`;
  if (f.min !== undefined) return `at least ${f.min}`;
  if (f.max !== undefined) return `at most ${f.max}`;
  return "";
}

function parseNumber(f: Field, raw: string): { v?: number; err?: string } {
  const n = Number(raw);
  if (!Number.isFinite(n)) return { err: "Enter a number" };
  if (f.type === "integer" || f.type === "bar") {
    if (!Number.isInteger(n)) return { err: "Enter a whole number" };
  }
  if ((f.min !== undefined && n < f.min) || (f.max !== undefined && n > f.max)) return { err: `Must be ${range(f)}` };
  return { v: n };
}

/**
 * The request body for a kind. Blank optional scalars are sent as null (the server default); a blank required field,
 * a non-number or an out-of-range value is a per-field error and `body` is null. Positions go out as `{bar}`,
 * downbeats as seconds or "auto", `dry_db` "off" as null (minus infinity) and blank as absent.
 */
export function buildRequest(caps: Pick<KindCaps, "kind" | "params">, form: FormState, ctx: FormContext): Built {
  const errors: FieldErrors = {};
  const params: Record<string, unknown> = {};
  const idOf = (name: string): string => (name === "track_b_id" ? (form.values.track_b_id ?? "").trim() : ctx.trackId);

  for (const p of caps.params) {
    if (p.name === "score") {
      params.score = { from_track_id: ctx.trackId };
      continue;
    }
    if (ID_PARAMS.has(p.name)) {
      const id = idOf(p.name);
      if (!id) errors[p.name] = "Choose a take";
      else if (p.name === "track_b_id" && id === ctx.trackId) errors[p.name] = "Choose a different take";
      params[p.name] = id;
    }
  }
  for (const f of fieldsFor(caps, ctx)) {
    if (f.key === "track_b_id") continue;
    const raw = (form.values[f.key] ?? "").trim();
    switch (f.type) {
      case "downbeat": {
        if (raw === "") {
          if (f.required) errors[f.key] = "Enter the downbeat or choose auto";
        } else if (raw === "auto") params[f.param] = "auto";
        else {
          const r = parseNumber({ ...f, max: undefined }, raw);
          if (r.err) errors[f.key] = r.err;
          else params[f.param] = r.v;
        }
        break;
      }
      case "bar": {
        if (raw !== "") {
          const r = parseNumber(f, raw);
          if (r.err) errors[f.key] = r.err;
          else params[f.param] = { bar: r.v };
        }
        break;
      }
      case "dryDb": {
        if (raw === "off") params[f.param] = null;
        else if (raw !== "") {
          const r = parseNumber(f, raw);
          if (r.err) errors[f.key] = r.err;
          else params[f.param] = r.v;
        }
        break;
      }
      case "select":
        if (raw === "") {
          if (f.required) errors[f.key] = "Choose one";
          params[f.param] = null;
        } else if (!(f.options ?? []).includes(raw)) errors[f.key] = "Choose one of the listed values";
        else params[f.param] = raw;
        break;
      case "boolean":
        params[f.param] = raw === "" ? null : raw === "true";
        break;
      case "number":
      case "integer": {
        if (raw === "") {
          if (f.required) errors[f.key] = "Required";
          params[f.param] = null;
        } else {
          const r = parseNumber(f, raw);
          if (r.err) errors[f.key] = r.err;
          else params[f.param] = r.v;
        }
        break;
      }
      default:
        if (raw === "") {
          if (f.required) errors[f.key] = "Required";
          params[f.param] = null;
        } else params[f.param] = raw;
    }
  }
  if (Object.keys(errors).length > 0) return { body: null, errors };
  const title = form.title.trim();
  const body = { kind: caps.kind, title: title === "" ? null : title, [caps.kind]: params } as unknown as TransformRequest;
  return { body, errors };
}

/** Stable key of a request body: the estimate is re-asked only when it changes. */
export const bodyKey = (b: TransformRequest): string => JSON.stringify(b);

/** Whether the "auto (guess)" toggle of a downbeat field is on. */
export const isAuto = (form: FormState, key: string): boolean => form.values[key] === "auto";

/** Presets for the ensemble space (voices are fixed): the four buttons of C214.2, from the enum of the `space` param. */
export function presetButtons(f: Field): string[] {
  return f.type === "select" ? (f.options ?? []) : [];
}

/** Whether the Render-score kind may be offered: the server lists it and this Take has a score. */
export function kindOffered(kind: string, listed: readonly string[], ctx: FormContext): boolean {
  if (!listed.includes(kind)) return false;
  return kind !== "score_render" || ctx.hasScore === true;
}
