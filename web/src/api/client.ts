// The only module allowed to touch the network (enforced by ESLint). Everything
// else in the SPA goes through the generated, typed client exported here.
import createClient, { type Middleware } from "openapi-fetch";
import type { paths } from "./schema";

export interface ApiOptions {
  fetch?: (req: Request) => Promise<Response>;
  reload?: () => void;
  /** Defaults to "" (same origin); tests outside a browser need an absolute URL. */
  baseUrl?: string;
}

function isApiRequest(req: Request): boolean {
  return new URL(req.url, "http://localhost").pathname.startsWith("/api/");
}

export function createApi(opts: ApiOptions = {}) {
  const reload = opts.reload ?? (() => location.reload());
  const mw: Middleware = {
    onRequest({ request }) {
      request.headers.set("X-MM-Client", "spa");
      request.headers.set("X-Requested-With", "XMLHttpRequest");
      return request;
    },
    onResponse({ response }) {
      // Session expired / proxy lost the identity: reload so the edge re-authenticates.
      if (response.status === 401) reload();
      return undefined;
    },
    onError({ request, error }) {
      if (error instanceof TypeError && isApiRequest(request)) reload();
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
