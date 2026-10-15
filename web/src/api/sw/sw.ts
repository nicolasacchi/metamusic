// Service worker entry (C115.1): precaches the shell, serves downloaded media from the offline store, and
// sends server-owned pages to the network. Classic script (built as an IIFE); no API URL is ever fetched here.
import { serveMedia } from "./respond";
import { route } from "./routes";
import { cacheStore } from "./store";

interface Precache {
  version: string;
  urls: string[];
}

// scripts/precache.ts replaces this token with {version, urls} after the build.
// (the property read keeps the minifier from folding the token away)
const injected: unknown = (self as unknown as { mmPrecache?: unknown }).mmPrecache ?? "__MM_PRECACHE__";
const PRECACHE: Precache =
  typeof injected === "object" && injected !== null ? (injected as Precache) : { version: "dev", urls: [] };
const SHELL_CACHE = `mm-shell-${PRECACHE.version}`;
const INDEX = "/index.html";

const sw = self as unknown as ServiceWorkerGlobalScope;
const store = cacheStore();

sw.addEventListener("install", (event) => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(SHELL_CACHE);
      await Promise.all(
        PRECACHE.urls.map(async (u) => {
          const res = await fetch(new Request(u, { cache: "reload" }));
          if (!res.ok) throw new Error(`precache ${u}: ${res.status}`);
          await cache.put(u, res);
        }),
      );
    })(),
  );
});

sw.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      // Only old shell caches go; mm-media-v1 and IndexedDB mm-offline are never touched by worker updates.
      for (const k of await caches.keys()) if (k.startsWith("mm-shell-") && k !== SHELL_CACHE) await caches.delete(k);
      await sw.clients.claim();
    })(),
  );
});

sw.addEventListener("message", (event) => {
  if ((event.data as { type?: string } | null)?.type === "skip-waiting") void sw.skipWaiting();
});

async function shell(req: Request): Promise<Response> {
  const cache = await caches.open(SHELL_CACHE);
  const hit = await cache.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  if (res.ok) await cache.put(req, res.clone());
  return res;
}

sw.addEventListener("fetch", (event) => {
  const req = event.request;
  const kind = route(new URL(req.url), req.method, req.mode, sw.location.origin);
  switch (kind) {
    case "media":
      event.respondWith(
        (async () => {
          try {
            const local = await serveMedia(req, store);
            if (local) return local;
          } catch {
            /* store unavailable: fall through to the network */
          }
          return fetch(req);
        })(),
      );
      return;
    case "shell":
      event.respondWith(shell(req));
      return;
    case "navigate-server":
      event.respondWith(fetch(req).catch(() => Response.redirect(new URL("/offline", sw.location.origin).href, 302)));
      return;
    case "navigate-spa":
      event.respondWith(
        fetch(req).catch(async () => {
          const cached = await (await caches.open(SHELL_CACHE)).match(INDEX);
          return cached ?? Response.error();
        }),
      );
      return;
    default:
      return; // pass: no respondWith
  }
});
