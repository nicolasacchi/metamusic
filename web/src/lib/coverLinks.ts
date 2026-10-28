// Cover links for the player and the track page (C115 Media Session artwork). Pure: the real wiring is cover.ts.

export interface CoverSet {
  url512: string;
  /** null for the cached offline cover (only the 512 px file is stored). */
  url1024: string | null;
}

export interface Artwork {
  src: string;
  sizes: string;
  type: string;
}

export const ICON_ARTWORK: Artwork = { src: "/icons/icon-512.png", sizes: "512x512", type: "image/png" };

/** Media Session artwork: the 512 and 1024 covers, else the app icon. */
export function artworkList(c: CoverSet | null): Artwork[] {
  if (!c) return [ICON_ARTWORK];
  const out: Artwork[] = [{ src: c.url512, sizes: "512x512", type: "image/png" }];
  if (c.url1024) out.push({ src: c.url1024, sizes: "1024x1024", type: "image/png" });
  return out;
}

export interface CoverDeps {
  /** get_media_links; null on failure. */
  links(trackId: string): Promise<{ cover: { url_512: string; url_1024: string; expires_at: string } | null } | null>;
  /** The cached 512 cover of a downloaded track. */
  local(trackId: string): Promise<string | null>;
  online(): boolean;
  now(): number;
  /** At most this many link requests in flight (default 3): a browser allows about six connections per host over HTTP/1.1, and the rest must stay free for the page's own calls. */
  maxConcurrent?: number;
}

/** Refresh a signed link this long before it expires. */
const MARGIN_MS = 60_000;

export function createCovers(deps: CoverDeps) {
  const cache = new Map<string, { set: CoverSet; until: number }>();
  const inflight = new Map<string, Promise<CoverSet | null>>();

  const fromLocal = async (id: string): Promise<CoverSet | null> => {
    try {
      const src = await deps.local(id);
      return src ? { url512: src, url1024: null } : null;
    } catch {
      return null;
    }
  };

  // A grid of tiles asks for many links at once and each one costs the server a cover render on a cold cache: they
  // queue here, first come first served, instead of taking every connection the browser has.
  const limit = deps.maxConcurrent ?? 3;
  let active = 0;
  const waiting: (() => void)[] = [];
  const slot = async (): Promise<void> => {
    if (active >= limit) await new Promise<void>((go) => waiting.push(go));
    else active++;
  };
  const release = () => {
    const next = waiting.shift();
    if (next) next(); // the slot passes straight to the next waiter
    else active--;
  };

  const load = async (id: string): Promise<CoverSet | null> => {
    if (!deps.online()) return fromLocal(id);
    try {
      await slot();
      let l: Awaited<ReturnType<CoverDeps["links"]>>;
      try {
        l = await deps.links(id);
      } finally {
        release();
      }
      if (l?.cover) {
        const set = { url512: l.cover.url_512, url1024: l.cover.url_1024 };
        const exp = Date.parse(l.cover.expires_at);
        cache.set(id, { set, until: (Number.isFinite(exp) ? exp : deps.now()) - MARGIN_MS });
        return set;
      }
      if (l) return null; // no cover service wired
    } catch {
      /* fall through to the cached cover */
    }
    return fromLocal(id);
  };

  return {
    async get(trackId: string): Promise<CoverSet | null> {
      const hit = cache.get(trackId);
      if (hit && deps.online() && hit.until > deps.now()) return hit.set;
      const f = inflight.get(trackId);
      if (f) return f;
      const p = load(trackId).finally(() => inflight.delete(trackId));
      inflight.set(trackId, p);
      return p;
    },
  };
}
