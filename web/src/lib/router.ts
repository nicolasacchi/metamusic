// Minimal History-API router: {path, params, query} store, link interception, 404.
import { writable, type Readable } from "svelte/store";

export interface Route {
  pattern: string;
  name: string;
}
export interface Location {
  path: string;
  query: Record<string, string>;
}
export interface Match {
  route: Route;
  params: Record<string, string>;
}
export interface RouterState extends Location {
  route: Route | null;
  params: Record<string, string>;
}

/** Every SPA route (C40); specific before parametric. `/human/*` pages are served by the Go shell, never client-navigated. */
export const APP_ROUTES: Route[] = [
  { pattern: "/", name: "home" },
  { pattern: "/tracks/:id", name: "track" },
  { pattern: "/backlog", name: "backlog" },
  { pattern: "/backlog/new", name: "draft-new" },
  { pattern: "/backlog/upload", name: "draft-upload" },
  { pattern: "/backlog/:id", name: "draft" },
  { pattern: "/playlists", name: "playlists" },
  { pattern: "/playlists/:id", name: "playlist" },
  { pattern: "/duels", name: "duels" },
  { pattern: "/duels/:id", name: "duel" },
  { pattern: "/ladder", name: "ladder" },
  { pattern: "/compare", name: "compare" },
  { pattern: "/export", name: "export" },
  { pattern: "/import", name: "import" },
  { pattern: "/settings", name: "settings" },
  { pattern: "/settings/backups", name: "settings-backups" },
  { pattern: "/settings/audit", name: "settings-audit" },
  { pattern: "/human/tokens", name: "human-tokens" },
  { pattern: "/human/devices", name: "human-devices" },
];

const SERVER_PREFIXES = ["/api", "/human", "/start", "/pair", "/auth", "/mcp", "/healthz", "/__e2e", "/.well-known", "/media", "/icons", "/manifest.webmanifest"];

/** Paths owned by the Go server; links to them must be left to the browser. */
export function isServerPath(path: string): boolean {
  return SERVER_PREFIXES.some((p) => path === p || path.startsWith(p + "/"));
}

function decode(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

export function parseLocation(href: string): Location {
  const noHash = href.split("#", 1)[0] ?? "";
  const q = noHash.indexOf("?");
  let path = q < 0 ? noHash : noHash.slice(0, q);
  const search = q < 0 ? "" : noHash.slice(q + 1);
  if (path.length > 1 && path.endsWith("/")) path = path.replace(/\/+$/, "") || "/";
  const query: Record<string, string> = {};
  for (const [k, v] of new URLSearchParams(search)) query[k] = v;
  return { path: path || "/", query };
}

export function matchRoute(routes: Route[], path: string): Match | null {
  const segs = path.split("/").filter(Boolean);
  for (const route of routes) {
    const pat = route.pattern.split("/").filter(Boolean);
    const params: Record<string, string> = {};
    let ok = true;
    for (let i = 0; i < pat.length && ok; i++) {
      const p = pat[i]!;
      if (p.startsWith("*")) {
        if (i >= segs.length) ok = false;
        else params[p.slice(1)] = segs.slice(i).map(decode).join("/");
        if (ok) return { route, params };
      } else if (p.startsWith(":")) {
        if (i >= segs.length) ok = false;
        else params[p.slice(1)] = decode(segs[i]!);
      } else if (segs[i] !== p) ok = false;
    }
    if (ok && pat.length === segs.length) return { route, params };
  }
  return null;
}

export function createRouter(routes: Route[]) {
  const state = writable<RouterState>(compute());

  function compute(): RouterState {
    const loc = parseLocation(window.location.pathname + window.location.search);
    const m = matchRoute(routes, loc.path);
    return { ...loc, route: m?.route ?? null, params: m?.params ?? {} };
  }
  function sync() {
    state.set(compute());
  }
  function navigate(to: string, replace = false, hash = "") {
    if (to + hash === window.location.pathname + window.location.search + window.location.hash) return;
    if (replace) history.replaceState(null, "", to + hash);
    else history.pushState(null, "", to + hash);
    sync();
  }
  function onClick(e: MouseEvent) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = (e.target as Element | null)?.closest?.("a");
    if (!a || a.target || a.hasAttribute("download") || a.relList.contains("external")) return;
    const href = a.getAttribute("href");
    if (!href || href.startsWith("#")) return;
    const url = new URL(a.href, window.location.href);
    if (url.origin !== window.location.origin) return;
    if (isServerPath(url.pathname)) return;
    e.preventDefault();
    navigate(url.pathname + url.search, false, url.hash);
  }
  window.addEventListener("popstate", sync);
  document.addEventListener("click", onClick);
  return {
    subscribe: state.subscribe as Readable<RouterState>["subscribe"],
    navigate,
    destroy() {
      window.removeEventListener("popstate", sync);
      document.removeEventListener("click", onClick);
    },
  };
}
