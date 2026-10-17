// The only module allowed to touch the network (enforced by ESLint). Everything
// else in the SPA goes through the generated, typed client exported here.
import createClient, { type Middleware } from "openapi-fetch";
import type { paths } from "./schema";
import { IDEMPOTENT, session as defaultSession, type Session } from "./session";
import { stepUp as defaultStepUp, type StepUp, type Trigger } from "./stepup";

/** Thrown for 429 RATE_LIMITED; never treated as session loss. */
export class RateLimited extends Error {
  readonly retryAfterS: number;
  constructor(retryAfterS: number, message = "Rate limited") {
    super(message);
    this.name = "RateLimited";
    this.retryAfterS = retryAfterS;
  }
}

const DEFAULT_RETRY_AFTER_S = 30;

function retryAfter(h: string | null): number {
  const n = h === null ? NaN : Number(h.trim());
  return Number.isFinite(n) && n >= 0 ? Math.ceil(n) : DEFAULT_RETRY_AFTER_S;
}

export interface ApiOptions {
  /** The step-up flow (second tab, retry in place); defaults to the app-wide one. */
  stepUp?: StepUp;
  fetch?: (req: Request) => Promise<Response>;
  /** Defaults to "" (same origin); tests outside a browser need an absolute URL. */
  baseUrl?: string;
  /** Session-loss handler (C22.4); defaults to the app-wide one. */
  session?: Session;
}

function isHumanApi(req: Request): boolean {
  return new URL(req.url, "http://localhost").pathname.startsWith("/human/api/");
}

function isApiRequest(req: Request): boolean {
  const p = new URL(req.url, "http://localhost").pathname;
  return p.startsWith("/api/") || p.startsWith("/human/api/");
}

async function problemCode(res: Response): Promise<string> {
  try {
    const b: unknown = await res.clone().json();
    return typeof b === "object" && b !== null && "code" in b ? String((b as { code: unknown }).code) : "";
  } catch {
    return "";
  }
}

export function createApi(opts: ApiOptions = {}) {
  const session = opts.session ?? defaultSession;
  const flow = opts.stepUp ?? defaultStepUp;
  const send = opts.fetch ?? ((req: Request) => globalThis.fetch(req));
  // A PUT body is consumed by the first attempt; keep a pristine copy for the one retry.
  const spare = new WeakMap<Request, Request>();
  const fresh = (req: Request) => (spare.get(req) ?? req).clone();
  // The app is reachable and this browser is signed in: a /human/api failure is then the gate, not an outage or a lost session.
  async function appReachable(req: Request): Promise<boolean> {
    try {
      const r = await send(new Request(new URL("/api/ping", req.url), { cache: "no-store", credentials: "same-origin" }));
      return r.status === 200;
    } catch {
      return false;
    }
  }
  // Wait for the second-tab confirmation, then replay the request here (the page, and its audio, never unload).
  async function afterStepUp(request: Request, kind: Trigger): Promise<Response | undefined> {
    if (!(await flow.require(kind))) return undefined;
    const r = await send(fresh(request));
    if (r.status < 500) session.ok();
    return r;
  }
  const mw: Middleware = {
    onRequest({ request }) {
      request.headers.set("X-MM-Client", "spa");
      request.headers.set("X-Requested-With", "XMLHttpRequest");
      // /human/api bodies are small JSON; uploads elsewhere are never cloned.
      if (request.body && (request.method === "PUT" || isHumanApi(request))) spare.set(request, request.clone());
      return request;
    },
    async onResponse({ request, response }) {
      if (!isApiRequest(request)) return undefined;
      if (response.status === 401) {
        // Production: the edge answers an unconfirmed /human/api call itself, with a 401 that is not ours.
        if (isHumanApi(request) && (await appReachable(request))) return afterStepUp(request, "edge");
        session.confirmed();
        return undefined;
      }
      session.ok();
      if (response.status === 429) {
        throw new RateLimited(retryAfter(response.headers.get("Retry-After")));
      }
      if (response.status === 403 && (await problemCode(response)) === "STEP_UP_REQUIRED") {
        return afterStepUp(request, "local");
      }
      return undefined;
    },
    async onError({ request, error }) {
      if (!(error instanceof TypeError) || !isApiRequest(request)) return undefined;
      // Production: the edge redirects an unconfirmed /human/api call to its login host; a cross-origin redirect fails the fetch.
      if (isHumanApi(request) && (await appReachable(request))) return afterStepUp(request, "edge");
      if (IDEMPOTENT.has(request.method)) {
        await session.retryDelay();
        try {
          // The retry's response flows through onResponse, which handles a 401.
          return await send(fresh(request));
        } catch (e) {
          if (!(e instanceof TypeError)) return undefined;
        }
      }
      await session.networkFailure();
      return undefined;
    },
  };
  const client = createClient<paths>({
    baseUrl: opts.baseUrl ?? "",
    ...(opts.fetch ? { fetch: opts.fetch } : {}),
  });
  client.use(mw);
  return client;
}

export const api = createApi();
