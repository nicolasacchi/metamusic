// API helpers for the T1 specs, in the style of l3Api.ts: every spec makes its own transforms through the operations the SPA
// uses, and identifies them by id, never by a total another spec could change. Nothing here deletes.
import type { APIRequestContext, Page } from "@playwright/test";
import { apiJSON } from "./helpers";
import type { TransformBody } from "./fixtures/t1";

export interface TView {
  id: string;
  take_id: string;
  kind: string;
  state: "queued" | "running" | "done" | "failed" | "cancelled";
  phase: string | null;
  progress_permille: number;
  title: string;
  spec_hash: string;
  reused: boolean;
  inputs: { role: string; track_id: string; title: string }[];
  warnings: { code: string }[];
  error: { code: string } | null;
  cancel_requested_at: string | null;
}
export interface TCaps {
  enabled: boolean;
  kinds: { kind: string; params: { name: string; type: string; required: boolean }[] }[];
  quota: { used_ms: number; reserved_ms: number; cap_ms: number };
  queue: { running: number; queued: number; max: number };
}
interface Wait {
  transform: TView;
  changed: boolean;
  timed_out: boolean;
}

export const LIVE_STATES = ["queued", "running"];

export const capabilities = (request: APIRequestContext) => apiJSON<TCaps>(request, "GET", "/api/transforms/capabilities");

export const createTransform = (request: APIRequestContext, body: TransformBody) => apiJSON<TView>(request, "POST", "/api/transforms", body, true);

export const getTransform = (request: APIRequestContext, id: string) => apiJSON<TView>(request, "GET", `/api/transforms/${id}`);

export const cancelTransform = (request: APIRequestContext, id: string) => apiJSON<TView>(request, "POST", `/api/transforms/${id}/cancel`, {}, true);

export async function listTransforms(request: APIRequestContext, query = ""): Promise<TView[]> {
  return (await apiJSON<{ items: TView[] }>(request, "GET", `/api/transforms?limit=100${query}`)).items;
}

/** Long-polls until the transform reaches one of `states` (default: any state that is not live). */
export async function waitFor(request: APIRequestContext, id: string, states: string[] = ["done", "failed", "cancelled"], timeoutMs = 120_000): Promise<TView> {
  const deadline = Date.now() + timeoutMs;
  let last = await getTransform(request, id);
  while (!states.includes(last.state)) {
    if (Date.now() > deadline) throw new Error(`transform ${id} did not reach ${states.join("/")} in ${timeoutMs} ms (state ${last.state}, phase ${last.phase})`);
    last = (await apiJSON<Wait>(request, "GET", `/api/transforms/${id}/wait?since_state=${last.state}&timeout_s=10`)).transform;
  }
  return last;
}

export async function createDone(request: APIRequestContext, body: TransformBody): Promise<TView> {
  const t = await createTransform(request, body);
  const done = await waitFor(request, t.id);
  if (done.state !== "done") throw new Error(`transform ${t.id} ended ${done.state} ${JSON.stringify(done.error)}`);
  return done;
}

/**
 * Collects what a clean run must not show: console errors, uncaught page errors and any 501/503 answer of a transform
 * operation (the "not available yet" state the panel shows for them). Call `.problems()` at the end of a test.
 */
export function watchT1(page: Page): { problems: () => string[] } {
  const found: string[] = [];
  page.on("pageerror", (e) => found.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    found.push(`console: ${m.text()}`);
  });
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/transforms") && (r.status() === 501 || r.status() === 503)) found.push(`${r.status()} ${r.request().method()} ${u.pathname}`);
  });
  return { problems: () => [...found] };
}

/**
 * The 44x44 px rule of helpers.smallTargets, limited to the elements inside `scope` (a CSS selector): the Take page around the
 * panel belongs to other lanes' specs. Same exemptions (hidden, off-screen, prose links, a checkbox counts by its label).
 */
export async function smallTargetsIn(page: Page, scope: string): Promise<{ what: string; w: number; h: number }[]> {
  return page.evaluate((scopeSel) => {
    const MIN = 43.5;
    const sel = 'a[href], button, input, select, textarea, summary, [role="button"], [role="tab"], [role="menuitem"], [tabindex]:not([tabindex="-1"])';
    const out: { what: string; w: number; h: number }[] = [];
    for (const el of document.querySelectorAll<HTMLElement>(sel)) {
      if (!el.closest(scopeSel)) continue;
      if (el instanceof HTMLInputElement && el.type === "hidden") continue;
      const cs = getComputedStyle(el);
      if (cs.visibility === "hidden" || cs.display === "none" || cs.pointerEvents === "none") continue;
      if (el.closest("[inert], [hidden], dialog:not([open])")) continue;
      const box: HTMLElement = el instanceof HTMLInputElement && (el.type === "checkbox" || el.type === "radio") ? (el.closest("label") ?? el) : el;
      const r = box.getBoundingClientRect();
      if (r.width <= 2 || r.height <= 2) continue;
      if (r.right <= 0 || r.bottom <= 0 || r.left >= innerWidth) continue;
      if (el.tagName === "A" && el.closest("p, li, td, span, label, h1, h2, h3") && !el.closest("nav, header, [role=menu]")) {
        const own = [...(el.parentElement?.childNodes ?? [])].filter((n) => n.nodeType === 3 && (n.textContent ?? "").trim() !== "").length;
        if (own > 0) continue;
      }
      if (r.width < MIN || r.height < MIN) {
        const name = (el.getAttribute("aria-label") ?? el.textContent ?? "").trim().replace(/\s+/g, " ").slice(0, 40);
        out.push({ what: `${el.tagName.toLowerCase()}[${el.getAttribute("data-testid") ?? ""}] "${name}"`, w: Math.round(r.width), h: Math.round(r.height) });
      }
    }
    return out;
  }, scope);
}
