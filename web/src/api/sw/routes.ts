// Pure request classification for the service worker (C115.1).
export type Route = "media" | "shell" | "navigate-spa" | "navigate-server" | "pass";
export type Mode = "navigate" | "same-origin" | "no-cors" | "cors" | "websocket";

// Built from segments so no `/api/` literal ends up in the worker bundle (swBuild.test.ts).
const NEVER = ["api", "human", "auth", "mcp", "rest", "worker", ".well-known", "__e2e", "healthz", "sw.js"].map((p) => "/" + p);
const SERVER_PAGES = ["start", "pair", "auth", "human"].map((p) => "/" + p);
const SHELL = ["assets", "icons"].map((p) => "/" + p);
const MEDIA = /^\/media\/([0-9a-f]{64})\.(flac|mp3|png)$/;

const under = (path: string, prefixes: string[]) => prefixes.some((p) => path === p || path.startsWith(p + "/"));

export function mediaKey(path: string): { sha: string; ext: "flac" | "mp3" | "png" } | null {
  const m = MEDIA.exec(path);
  return m ? { sha: m[1]!, ext: m[2] as "flac" | "mp3" | "png" } : null;
}

export function route(url: URL, method: string, mode: Mode | string, origin = url.origin): Route {
  if (method !== "GET" || url.origin !== origin) return "pass";
  const path = url.pathname;
  if (mode === "navigate" && under(path, SERVER_PAGES)) return "navigate-server";
  if (under(path, NEVER)) return "pass";
  if (mediaKey(path)) return "media";
  if (under(path, SHELL) || path === "/manifest.webmanifest") return "shell";
  return mode === "navigate" ? "navigate-spa" : "pass";
}
