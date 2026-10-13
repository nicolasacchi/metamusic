// Pure helpers for the draft editor: form <-> request, canonical preview, dirty detection,
// debounced lint, save / keep-mine flows and lint/upload view models.
import type { createApi } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { components } from "../api/schema";

type Api = ReturnType<typeof createApi>;
type DraftDetail = components["schemas"]["DraftDetail"];
type DraftRequest = components["schemas"]["DraftRequest"];
type Issue = components["schemas"]["Issue"];
type LintResult = components["schemas"]["LintResult"];
type UploadResult = components["schemas"]["UploadResult"];

export interface DraftForm {
  id: string;
  style: string;
  lyrics: string;
  cot: string;
  /** Kept as text so a half-typed number survives editing. */
  seed: string;
  cfgScale: string;
  abc: string;
  label: string;
}

export function emptyForm(): DraftForm {
  return { id: "", style: "", lyrics: "", cot: "", seed: "0", cfgScale: "", abc: "", label: "" };
}

export function formFromRequest(r: Partial<DraftRequest>, label: string | null = null): DraftForm {
  return {
    id: r.id ?? "",
    style: r.style ?? "",
    lyrics: r.lyrics ?? "",
    cot: r.cot ?? "",
    seed: r.seed === undefined ? "0" : String(r.seed),
    cfgScale: r.cfg_scale === undefined ? "" : String(r.cfg_scale),
    abc: r.abc ?? "",
    label: label ?? "",
  };
}

function num(s: string): number | undefined {
  const t = s.trim();
  if (t === "") return undefined;
  const n = Number(t);
  return Number.isFinite(n) ? n : undefined;
}

/** The request object sent to the API; abc and cfg_scale are optional and omitted when empty. */
export function toRequest(f: DraftForm): Record<string, unknown> {
  const out: Record<string, unknown> = { id: f.id, style: f.style, lyrics: f.lyrics, cot: f.cot };
  const seed = num(f.seed);
  if (seed !== undefined) out.seed = seed;
  const cfg = num(f.cfgScale);
  if (cfg !== undefined) out.cfg_scale = cfg;
  if (f.abc !== "") out.abc = f.abc;
  return out;
}

function sortKeys(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(sortKeys);
  if (v !== null && typeof v === "object") {
    const o = v as Record<string, unknown>;
    return Object.fromEntries(Object.keys(o).sort().map((k) => [k, sortKeys(o[k])]));
  }
  return v;
}

/** Canonical-looking JSON preview: keys sorted at every depth. */
export function preview(f: DraftForm): string {
  return JSON.stringify(sortKeys(toRequest(f)), null, 2);
}

export function labelOrUndefined(f: DraftForm): string | undefined {
  const l = f.label.trim();
  return l === "" ? undefined : l;
}

export function isDirty(initial: DraftForm, current: DraftForm): boolean {
  return JSON.stringify(sortKeys(toRequest(initial))) !== JSON.stringify(sortKeys(toRequest(current))) || initial.label.trim() !== current.label.trim();
}

// ---- debounced lint -------------------------------------------------------------------------

export interface Timers {
  set: (fn: () => void, ms: number) => unknown;
  clear: (h: unknown) => void;
}
const realTimers: Timers = { set: (fn, ms) => setTimeout(fn, ms), clear: (h) => clearTimeout(h as ReturnType<typeof setTimeout>) };

export const LINT_DEBOUNCE_MS = 400;

/** Trailing debounce: only the last call inside the window runs. */
export function debounced<A extends unknown[]>(fn: (...a: A) => void, ms = LINT_DEBOUNCE_MS, timers: Timers = realTimers) {
  let h: unknown = null;
  const call = (...a: A) => {
    if (h !== null) timers.clear(h);
    h = timers.set(() => {
      h = null;
      fn(...a);
    }, ms);
  };
  call.cancel = () => {
    if (h !== null) timers.clear(h);
    h = null;
  };
  return call;
}

/** Lints one request; returns the item result, or null when the call failed. */
export async function lintForm(api: Api, f: DraftForm, draftId?: string): Promise<LintResult | null> {
  const r = await api.POST("/api/recipes/lint", {
    body: { items: [{ request: toRequest(f) as Record<string, never>, ...(draftId ? { draft_id: draftId } : {}) }], batch: false },
  });
  return r.data?.items[0] ?? null;
}

// ---- save / conflict ------------------------------------------------------------------------

export type SaveOutcome =
  | { kind: "ok"; draft: DraftDetail }
  | { kind: "conflict" }
  | { kind: "error"; message: string };

export async function saveDraft(api: Api, id: string, revision: number, f: DraftForm): Promise<SaveOutcome> {
  const label = f.label.trim();
  const r = await api.PATCH("/api/drafts/{id}", {
    params: { path: { id } },
    body: { revision, request: toRequest(f) as Record<string, never>, label: label === "" ? null : label },
  });
  if (r.data) return { kind: "ok", draft: r.data };
  if (r.response.status === 409 && r.error?.code === "REVISION_CONFLICT") return { kind: "conflict" };
  return { kind: "error", message: r.error?.message ?? "Could not save the draft" };
}

/** "Keep mine" after a conflict: the user's text becomes a new draft under a fresh idempotency key. */
export async function keepMine(api: Api, f: DraftForm, parentTrackId?: string | null): Promise<SaveOutcome> {
  const label = labelOrUndefined(f);
  const send = idempotentAction(async (key) =>
    api.POST("/api/drafts", {
      params: { header: { "Idempotency-Key": key } },
      body: {
        request: toRequest(f) as Record<string, never>,
        ...(label ? { label } : {}),
        ...(parentTrackId ? { parent_track_id: parentTrackId } : {}),
      },
    }),
  );
  const r = await send();
  return r.data ? { kind: "ok", draft: r.data } : { kind: "error", message: r.error?.message ?? "Could not create the draft" };
}

export async function setDraftStatus(api: Api, d: DraftDetail, status: "backlog" | "archived"): Promise<SaveOutcome> {
  const r = await api.PATCH("/api/drafts/{id}", { params: { path: { id: d.id } }, body: { revision: d.revision, status } });
  if (r.data) return { kind: "ok", draft: r.data };
  if (r.response.status === 409) return { kind: "conflict" };
  return { kind: "error", message: r.error?.message ?? "Could not update the draft" };
}

// ---- lint view model -------------------------------------------------------------------------

export interface IssueView {
  severity: "error" | "warning";
  code: string;
  text: string;
  /** 1-based line in the lyrics textarea, when the issue names one. */
  line?: number;
  field?: string;
  droppedKey?: string;
}

export function issueView(i: Issue, severity: "error" | "warning"): IssueView {
  const v: IssueView = { severity, code: i.code, text: i.message };
  if (i.line !== undefined && i.line > 0) v.line = i.line;
  if (i.field) v.field = i.field;
  if (i.code.startsWith("dropped_key:")) {
    v.droppedKey = i.code.slice("dropped_key:".length);
    v.severity = "warning";
    if (!v.text) v.text = `Unknown key "${v.droppedKey}" will be dropped`;
  }
  return v;
}

export function lintIssues(l: LintResult | null): IssueView[] {
  if (!l) return [];
  return [...l.errors.map((i) => issueView(i, "error")), ...l.warnings.map((i) => issueView(i, "warning"))];
}

/** Character range [start, end) of a 1-based line in the text, for selecting it in the textarea. */
export function lineRange(text: string, line: number): [number, number] {
  const lines = text.split("\n");
  const idx = Math.min(Math.max(line, 1), lines.length) - 1;
  let start = 0;
  for (let k = 0; k < idx; k++) start += lines[k]!.length + 1;
  return [start, start + lines[idx]!.length];
}

// ---- upload view model -----------------------------------------------------------------------

export interface UploadRow {
  line: number;
  state: "created" | "error" | "warning";
  text: string;
}

export function uploadRows(r: UploadResult): UploadRow[] {
  const rows: UploadRow[] = [
    ...r.drafts.map((d): UploadRow => ({ line: d.line, state: "created", text: d.label ?? d.request_id })),
    ...r.errors.map((e): UploadRow => ({ line: e.line, state: "error", text: e.field ? `${e.code} (${e.field})` : e.code })),
    ...r.warnings.map((e): UploadRow => ({ line: e.line, state: "warning", text: e.field ? `${e.code} (${e.field})` : e.code })),
  ];
  const order = { error: 0, warning: 1, created: 2 };
  return rows.sort((a, b) => a.line - b.line || order[a.state] - order[b.state]);
}

/** Per-line error rows of a rejected upload (422 LINT_FAILED carries `details.items`); [] for any other error. */
export function failedRows(body: unknown): UploadRow[] {
  const items = (body as { details?: { items?: unknown } } | undefined)?.details?.items;
  if (!Array.isArray(items)) return [];
  const rows: UploadRow[] = [];
  for (const it of items as { line?: number; errors?: { code: string; field?: string }[] }[]) {
    for (const e of it.errors ?? []) rows.push({ line: it.line ?? 0, state: "error", text: e.field ? `${e.code} (${e.field})` : e.code });
  }
  return rows.sort((a, b) => a.line - b.line);
}
