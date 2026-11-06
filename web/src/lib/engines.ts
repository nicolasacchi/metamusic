// The engine picker's data (E2): list_engines, which answers 501 until the engines operation lands.
// A 501 (or any failure) is not an error for the page: it keeps working with yue2 only.
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ENGINE_MODES, type EngineMode, type EngineName } from "./genIntent";

export type EngineView = components["schemas"]["EngineView"];
export type EngineCaps = components["schemas"]["EngineCaps"];
export type EngineList = components["schemas"]["EngineList"];

export type EnginesResult = { kind: "ok"; list: EngineList } | { kind: "unavailable" } | { kind: "error"; message: string };

/** One GET of list_engines. Callers fetch only when the user can act on the answer (the picker is opened). */
export async function fetchEngines(): Promise<EnginesResult> {
  try {
    const r = await api.GET("/api/engines", {});
    if (r.data) return { kind: "ok", list: r.data };
    if (r.response.status === 501 || r.response.status === 503) return { kind: "unavailable" };
    return { kind: "error", message: r.error?.message ? `${r.error.code}: ${r.error.message}` : "Could not load the engines." };
  } catch {
    return { kind: "error", message: "Network error while loading the engines." };
  }
}

const REASONS: Record<string, string> = {
  not_enabled: "Not enabled on this server.",
  not_adopted: "Not adopted yet.",
  backend_absent: "No backend is configured for it.",
  backend_disabled: "Its backend is disabled.",
  disarmed: "Its backend is disarmed.",
  tripped: "Its backend is tripped.",
};

/** The human reason an engine cannot be picked, or "" when it can. */
export function unavailableText(e: EngineView): string {
  if (e.selectable) return "";
  const base = (e.unavailable_reason && REASONS[e.unavailable_reason]) || "Not available right now.";
  if (e.unavailable_reason === "backend_disabled" && e.backend?.disabled_reason) return `${base} (${e.backend.disabled_reason.replaceAll("_", " ")})`;
  if (e.unavailable_reason === "tripped" && e.backend?.trip_reason) return `${base} (${e.backend.trip_reason})`;
  return base;
}

export interface PickerChoice {
  name: string;
  label: string;
  /** Pickable now: selectable by the server and allowed for the intent. */
  enabled: boolean;
  /** Why not, or "". */
  reason: string;
  /** How to adopt, for a not adopted engine. */
  hint: string;
}

/**
 * The choices for the picker. Only a selectable engine is enabled; others stay visible with the reason. `allowed`
 * lets the page rule an engine out for the current intent (an ACE-Step-only action cannot go to yue2).
 */
export function pickerChoices(list: EngineList, allowed: (name: string) => string = () => ""): PickerChoice[] {
  return list.engines.map((e) => {
    const server = unavailableText(e);
    const local = server === "" ? allowed(e.name) : "";
    return {
      name: e.name,
      label: e.label,
      enabled: server === "" && local === "",
      reason: server || local,
      hint: e.unavailable_reason === "not_adopted" && e.adopt_hint ? e.adopt_hint : "",
    };
  });
}

/** The caps of an engine by name, or null. */
export function capsOf(list: EngineList | null, name: string): EngineCaps | null {
  return list?.engines.find((e) => e.name === name)?.caps ?? null;
}

/** The ACE-Step modes an engine offers (its caps' tasks); an unknown engine offers all, the server decides. */
export function modesOffered(caps: EngineCaps | null): EngineMode[] {
  if (!caps) return [...ENGINE_MODES];
  const flags: Record<EngineMode, boolean> = {
    port: caps.text2song,
    reference: caps.reference_audio,
    repaint: caps.repaint_interval,
    extend: caps.extend,
    complete: caps.complete,
    stems: caps.stems,
  };
  return ENGINE_MODES.filter((m) => flags[m]);
}

/** Why an engine mode is off for the engine, or "". */
export function modeUnavailable(mode: EngineMode, caps: EngineCaps | null): string {
  return modesOffered(caps).includes(mode) ? "" : `This engine does not offer ${mode}.`;
}

/** "Choose the engine" summary line: the label of the engine in use. */
export function engineLabel(list: EngineList | null, name: EngineName): string {
  return list?.engines.find((e) => e.name === name)?.label ?? (name === "yue2" ? "YuE2" : "ACE-Step");
}

const WARNING_HINTS: Record<string, string> = {
  cross_engine_edit: "This edits a take made by another engine, so the result will differ from the original.",
  extend_seam_experimental: "Extending joins new audio to the old; the seam can be audible.",
  base_class_cost: "The base model class costs more per second than turbo.",
  reference_not_reproducible: "A render with a reference is not reproducible from its seed.",
};

/** A plain-language line for the E2 warnings the estimate card calls out; "" for any other code. */
export function warningHint(code: string): string {
  return WARNING_HINTS[code] ?? "";
}

type Take = components["schemas"]["TakePlan"];

/** The engine details of one planned take for the estimate table: task (class), range and reference. "" for a plain yue2 take. */
export function takeDetail(t: Take): string {
  const parts: string[] = [];
  if (t.task) parts.push(t.task_class ? `${t.task} (${t.task_class})` : t.task);
  const e = t.edit;
  if (e && e.from_s != null && e.to_s != null) parts.push(`${e.from_s}–${e.to_s} s`);
  if (e && e.extend_s != null) parts.push(`+${e.extend_s} s`);
  if (e?.tracks?.length) parts.push(e.tracks.join(", "));
  if (e?.stem) parts.push(`stem ${e.stem}`);
  if (t.reference) parts.push(`reference ${t.reference.from_s}–${t.reference.to_s} s`);
  if (t.cross_engine) parts.push("cross engine");
  return parts.join(" · ");
}

/** The task classes of an estimate's takes, distinct and in order ("turbo", "base"). */
export function taskClasses(takes: readonly Take[]): string[] {
  return [...new Set(takes.flatMap((t) => (t.task_class ? [t.task_class] : [])))];
}
