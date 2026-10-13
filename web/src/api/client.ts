// The only module allowed to touch the network (enforced by ESLint). Everything
// else in the SPA goes through the generated, typed client exported here.
import createClient, { type Middleware } from "openapi-fetch";
import type { paths } from "./schema";
import { IDEMPOTENT, session as defaultSession, type Session } from "./session";

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
  /** Full-page navigation (step-up redirect); defaults to location.assign. */
  assign?: (url: string) => void;
  /** Path + query of the current page, used as the step-up `next`. */
  currentPath?: () => string;
  fetch?: (req: Request) => Promise<Response>;
  /** Defaults to "" (same origin); tests outside a browser need an absolute URL. */
  baseUrl?: string;
  /** Session-loss handler (C22.4); defaults to the app-wide one. */
  session?: Session;
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
  const send = opts.fetch ?? ((req: Request) => globalThis.fetch(req));
  // A PUT body is consumed by the first attempt; keep a pristine copy for the one retry.
  const spare = new WeakMap<Request, Request>();
  const mw: Middleware = {
    onRequest({ request }) {
      request.headers.set("X-MM-Client", "spa");
      request.headers.set("X-Requested-With", "XMLHttpRequest");
      if (request.method === "PUT" && request.body) spare.set(request, request.clone());
      return request;
    },
    async onResponse({ request, response }) {
      if (!isApiRequest(request)) return undefined;
      if (response.status === 401) {
        session.confirmed();
        return undefined;
      }
      session.ok();
      if (response.status === 429) {
        throw new RateLimited(retryAfter(response.headers.get("Retry-After")));
      }
      if (response.status === 403 && (await problemCode(response)) === "STEP_UP_REQUIRED") {
        const here = opts.currentPath?.() ?? location.pathname + location.search;
        (opts.assign ?? ((u) => location.assign(u)))("/auth/step-up?next=" + encodeURIComponent(here));
      }
      return undefined;
    },
    async onError({ request, error }) {
      if (!(error instanceof TypeError) || !isApiRequest(request)) return undefined;
      if (IDEMPOTENT.has(request.method)) {
        await session.retryDelay();
        try {
          // The retry's response flows through onResponse, which handles a 401.
          return await send(spare.get(request) ?? request.clone());
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
