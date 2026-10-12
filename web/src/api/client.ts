// The only module allowed to touch the network (enforced by ESLint). Everything
// else in the SPA goes through the generated, typed client exported here.
import createClient, { type Middleware } from "openapi-fetch";
import { writable } from "svelte/store";
import type { paths } from "./schema";

/** True when an automatic session-recovery reload was suppressed by the throttle. */
export const connectionLost = writable(false);

const RELOAD_WINDOW_MS = 10_000;
const RELOAD_KEY = "mm:last-reload";

export interface ApiOptions {
  fetch?: (req: Request) => Promise<Response>;
  reload?: () => void;
  /** Defaults to "" (same origin); tests outside a browser need an absolute URL. */
  baseUrl?: string;
  /** Reload-throttle clock and store; default Date.now / sessionStorage. */
  now?: () => number;
  storage?: Pick<Storage, "getItem" | "setItem">;
}

function isApiRequest(req: Request): boolean {
  const p = new URL(req.url, "http://localhost").pathname;
  return p.startsWith("/api/") || p.startsWith("/human/api/");
}

export function createApi(opts: ApiOptions = {}) {
  const doReload = opts.reload ?? (() => location.reload());
  const now = opts.now ?? Date.now;
  const storage = opts.storage ?? (typeof sessionStorage === "undefined" ? undefined : sessionStorage);
  // At most one automatic reload per window; otherwise surface a banner instead of looping.
  const reload = () => {
    const t = now();
    let last = 0;
    try {
      last = Number(storage?.getItem(RELOAD_KEY) ?? 0) || 0;
    } catch {
      /* storage unavailable: treat as never reloaded */
    }
    if (t - last < RELOAD_WINDOW_MS) {
      connectionLost.set(true);
      return;
    }
    try {
      storage?.setItem(RELOAD_KEY, String(t));
    } catch {
      /* ignore */
    }
    doReload();
  };
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
