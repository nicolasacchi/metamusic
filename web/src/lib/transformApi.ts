// Typed wrappers over the seven transform operations plus the two small state machines the Take panel runs on top of
// them: the debounced estimate and the create -> follow -> cancel render. Network only through the generated client
// (`Api` is injected, so bun tests pass a mock); timers and sleeping are injectable so tests use a fake clock.
import { RateLimited, type createApi } from "../api/client";
import { newIdempotencyKey } from "../api/idem";
import type { components, operations } from "../api/schema";
import type { TransformRequest } from "./transformForm";
import { bodyKey } from "./transformForm";
import { isLive, type Refusal, type TransformEstimate, type TransformView } from "./transformView";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];
export type Capabilities = S["TransformCapabilities"];
export type TransformPage = S["TransformPage"];
export type TransformWait = S["TransformWait"];
export type ListQuery = NonNullable<operations["list_transforms"]["parameters"]["query"]>;

export type TRes<T> = { ok: true; status: number; value: T } | ({ ok: false } & Refusal);

interface Raw<T> {
  data?: T;
  error?: unknown;
  response: { status: number };
}

function refusalOf(status: number, e: unknown): Refusal {
  const o = typeof e === "object" && e !== null ? (e as { code?: unknown; message?: unknown; details?: unknown }) : {};
  const details = typeof o.details === "object" && o.details !== null ? (o.details as Record<string, unknown>) : {};
  const code = typeof o.code === "string" && o.code !== "" ? o.code : status === 501 ? "NOT_IMPLEMENTED" : "";
  return { status, code, message: typeof o.message === "string" ? o.message : "", details };
}

/** Runs one call: success, a problem body, a client-side 429 (RateLimited), or a network failure. Never throws. */
async function call<T>(run: () => Promise<Raw<T>>): Promise<TRes<T>> {
  try {
    const r = await run();
    if (r.data !== undefined) return { ok: true, status: r.response.status, value: r.data };
    return { ok: false, ...refusalOf(r.response.status, r.error) };
  } catch (e) {
    if (e instanceof RateLimited) return { ok: false, status: 429, code: "RATE_LIMITED", message: e.message, details: {}, retryAfterS: e.retryAfterS };
    return { ok: false, status: 0, code: "NETWORK", message: "Network error", details: {} };
  }
}

export const getCapabilities = (api: Api): Promise<TRes<Capabilities>> => call(() => api.GET("/api/transforms/capabilities", {}));
export const estimateTransform = (api: Api, body: TransformRequest): Promise<TRes<TransformEstimate>> => call(() => api.POST("/api/transforms/estimate", { body }));
/** The caller owns the key: one per Render click, reused by a retry of that click. */
export const createTransform = (api: Api, body: TransformRequest, key: string): Promise<TRes<TransformView>> =>
  call(() => api.POST("/api/transforms", { params: { header: { "Idempotency-Key": key } }, body }));
export const getTransform = (api: Api, id: string): Promise<TRes<TransformView>> => call(() => api.GET("/api/transforms/{id}", { params: { path: { id } } }));
export const waitForTransform = (api: Api, id: string, since: TransformView["state"], timeoutS: number): Promise<TRes<TransformWait>> =>
  call(() => api.GET("/api/transforms/{id}/wait", { params: { path: { id }, query: { since_state: since, timeout_s: timeoutS } } }));
export const cancelTransform = (api: Api, id: string): Promise<TRes<TransformView>> => call(() => api.POST("/api/transforms/{id}/cancel", { params: { path: { id } } }));
export const listTransforms = (api: Api, query: ListQuery): Promise<TRes<TransformPage>> => call(() => api.GET("/api/transforms", { params: { query } }));

export interface PartnerTake {
  id: string;
  title: string;
}
/** "Same lyrics, other styles": search_tracks {lyrics_of}, minus the Take itself. */
export async function sameLyricsTakes(api: Api, trackId: string): Promise<TRes<PartnerTake[]>> {
  const r = await call(() => api.GET("/api/tracks", { params: { query: { lyrics_of: trackId, limit: 50 } as never } }));
  if (!r.ok) return r;
  return { ok: true, status: r.status, value: r.value.items.filter((t) => t.id !== trackId).map((t) => ({ id: t.id, title: t.title })) };
}

// ---- debounced estimate --------------------------------------------------------------------------------------

export interface Scheduler {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}
export const realScheduler: Scheduler = { set: (fn, ms) => setTimeout(fn, ms), clear: (h) => clearTimeout(h as ReturnType<typeof setTimeout>) };
export const ESTIMATE_DEBOUNCE_MS = 400;

export type EstimateState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "ok"; estimate: TransformEstimate }
  | { kind: "refused"; refusal: Refusal }
  | { kind: "unavailable"; refusal: Refusal };

export interface EstimateOptions {
  ask: (body: TransformRequest) => Promise<TRes<TransformEstimate>>;
  onState: (s: EstimateState) => void;
  scheduler?: Scheduler;
  delayMs?: number;
}

/**
 * Estimates the latest request 400 ms after the last change. A body equal to the last one asked is not asked again,
 * `null` (the form is not valid yet) goes idle, and an answer for an older request is dropped.
 */
export class EstimateController {
  private handle: unknown = null;
  private seq = 0;
  private lastKey = "";
  private readonly sched: Scheduler;
  private readonly delay: number;
  constructor(private readonly o: EstimateOptions) {
    this.sched = o.scheduler ?? realScheduler;
    this.delay = o.delayMs ?? ESTIMATE_DEBOUNCE_MS;
  }

  request(body: TransformRequest | null): void {
    this.cancelTimer();
    if (body === null) {
      this.seq++;
      this.lastKey = "";
      this.o.onState({ kind: "idle" });
      return;
    }
    const key = bodyKey(body);
    if (key === this.lastKey) return;
    this.lastKey = key;
    const mine = ++this.seq;
    this.o.onState({ kind: "loading" });
    this.handle = this.sched.set(() => {
      this.handle = null;
      void this.o.ask(body).then((r) => {
        if (mine !== this.seq) return;
        if (r.ok) this.o.onState({ kind: "ok", estimate: r.value });
        else {
          // a failed estimate is asked again on the next change, and by retry()
          this.lastKey = "";
          this.o.onState(isUnavail(r) ? { kind: "unavailable", refusal: r } : { kind: "refused", refusal: r });
        }
      });
    }, this.delay);
  }

  /** Forget the last body so an unchanged form asks again. */
  retry(): void {
    this.lastKey = "";
  }
  dispose(): void {
    this.cancelTimer();
    this.seq++;
  }
  private cancelTimer(): void {
    if (this.handle !== null) this.sched.clear(this.handle);
    this.handle = null;
  }
}
const isUnavail = (r: Refusal): boolean => r.status === 501 || r.code === "NOT_IMPLEMENTED" || (r.status === 503 && r.code === "UNAVAILABLE" && r.details.component === "transform");

// ---- render: create, follow, cancel ---------------------------------------------------------------------------

export type RenderState =
  | { kind: "idle" }
  | { kind: "creating" }
  | { kind: "live"; view: TransformView }
  | { kind: "finished"; view: TransformView }
  | { kind: "refused"; refusal: Refusal }
  | { kind: "unavailable"; refusal: Refusal };

export interface RenderOptions {
  create: (body: TransformRequest, key: string) => Promise<TRes<TransformView>>;
  wait: (id: string, since: TransformView["state"], timeoutS: number) => Promise<TRes<TransformWait>>;
  cancel: (id: string) => Promise<TRes<TransformView>>;
  onState: (s: RenderState) => void;
  sleep?: (ms: number) => Promise<void>;
  newKey?: () => string;
  /** Long-poll length; short so the progress bar moves (progress alone does not end a wait). */
  pollS?: number;
  /** Consecutive failed polls before the follower gives up (the transform keeps running on the server). */
  maxFailures?: number;
}
const realSleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export class RenderController {
  private token = 0;
  private key = "";
  private view: TransformView | null = null;
  private readonly sleep: (ms: number) => Promise<void>;
  constructor(private readonly o: RenderOptions) {
    this.sleep = o.sleep ?? realSleep;
  }

  /** One Render click: a fresh idempotency key, then follow the transform to its end. */
  async start(body: TransformRequest): Promise<void> {
    const mine = ++this.token;
    this.key = (this.o.newKey ?? newIdempotencyKey)();
    this.o.onState({ kind: "creating" });
    const r = await this.o.create(body, this.key);
    if (mine !== this.token) return;
    if (!r.ok) return this.refuse(r);
    await this.track(r.value, mine);
  }

  /** Follow an existing transform (resumed on page load). */
  async follow(view: TransformView): Promise<void> {
    await this.track(view, ++this.token);
  }

  /** Cancel the followed transform; the follower then sees the terminal state. */
  async cancelNow(): Promise<void> {
    const v = this.view;
    if (!v || !isLive(v.state)) return;
    const r = await this.o.cancel(v.id);
    if (!r.ok) return this.refuse(r);
    this.view = r.value;
    this.emitView(r.value);
  }

  /** Stop following (the page left); the transform itself keeps going. */
  stop(): void {
    this.token++;
  }

  reset(): void {
    this.token++;
    this.view = null;
    this.o.onState({ kind: "idle" });
  }

  private refuse(r: { ok: false } & Refusal): void {
    this.o.onState(isUnavail(r) ? { kind: "unavailable", refusal: r } : { kind: "refused", refusal: r });
  }
  private emitView(v: TransformView): void {
    this.o.onState(isLive(v.state) ? { kind: "live", view: v } : { kind: "finished", view: v });
  }

  private async track(first: TransformView, mine: number): Promise<void> {
    this.view = first;
    this.emitView(first);
    let failures = 0;
    const maxFailures = this.o.maxFailures ?? 5;
    while (mine === this.token && this.view && isLive(this.view.state)) {
      const r = await this.o.wait(this.view.id, this.view.state, this.o.pollS ?? 8);
      if (mine !== this.token) return;
      if (r.ok) {
        failures = 0;
        this.view = r.value.transform;
        this.emitView(r.value.transform);
        continue;
      }
      if (r.status === 404 || r.status === 403 || r.status === 501) return this.refuse(r);
      if (++failures >= maxFailures) return this.refuse(r);
      await this.sleep(Math.min(15000, 1000 * 2 ** failures));
    }
  }
}
