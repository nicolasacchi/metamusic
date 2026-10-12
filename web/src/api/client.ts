// The only module allowed to touch the network (enforced by ESLint). Everything
// else in the SPA goes through the generated, typed client exported here.
import createClient, { type Middleware } from "openapi-fetch";
import type { paths } from "./schema";
import { IDEMPOTENT, session as defaultSession, type Session } from "./session";

export interface ApiOptions {
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
    onResponse({ request, response }) {
      if (!isApiRequest(request)) return undefined;
      if (response.status === 401) session.confirmed();
      else session.ok();
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
