// Morning Crate side links for the player (M6N-W2, C384.2). A crate side is one lossless file with many chapters; the
// player treats `morning:<crate>:<side>` queue items like tape items: one FLAC, no listen events, no take page.
// The signed links ride in get_morning_crate, so the crate view is the one source of both links and chapter names.
import { api } from "../api/client";
import type { MediaLinks } from "./player.svelte";
import { chapterAt, chapterTitle, parseMorningItem, sideLabel, sideUrl, type MorningCrate } from "./morningCrate";

const views = new Map<string, MorningCrate>();
/** Items whose links were handed out once: a second request means the first ones failed or ran out, so the crate is read again. */
const served = new Set<string>();

export function rememberCrate(view: MorningCrate): void {
  views.set(view.id, view);
}

export const knownCrate = (id: string): MorningCrate | undefined => views.get(id);

/** Test seam: forget everything. */
export function forgetCrates(): void {
  views.clear();
  served.clear();
}

export async function fetchCrate(id: string): Promise<MorningCrate | null> {
  try {
    const r = await api.GET("/api/morning-crates/{id}", { params: { path: { id } } });
    return r.data ?? null;
  } catch {
    return null;
  }
}

/** `fetchLinks` for a `morning:<crate>:<side>` item. A media error re-mints once: that second call re-fetches get_morning_crate. */
export async function morningLinks(item: string, get: (id: string) => Promise<MorningCrate | null> = fetchCrate, now: () => number = Date.now): Promise<MediaLinks | null> {
  const it = parseMorningItem(item);
  if (!it) return null;
  let view = views.get(it.crate);
  const side = () => view?.sides.find((s) => s.side === it.side);
  if (!view || served.has(item) || !sideUrl(side(), now())) {
    const fresh = await get(it.crate);
    if (fresh) {
      view = fresh;
      views.set(fresh.id, fresh);
    }
  }
  served.add(item);
  const s = side();
  const url = sideUrl(s, now());
  if (!s || !url) return null;
  const exp = s.expires_at && Number.isFinite(Date.parse(s.expires_at)) ? Date.parse(s.expires_at) : now() + 30 * 60_000;
  const flac = { url, bytes: s.bytes, content_type: "audio/flac", sample_rate: 0, bits_per_sample: 24, sha256: s.sha256 };
  return {
    track_id: item,
    expires_at: new Date(exp).toISOString(),
    ttl_s: Math.max(0, Math.round((exp - now()) / 1000)),
    cover: null,
    lossy: false,
    flac,
    // Unreachable: a crate side is never played as MP3. Present because the link shape requires it.
    mp3: { url, bytes: s.bytes, content_type: "audio/flac", bitrate_kbps: 0, sha256: s.sha256 },
  };
}

/** `fetchMeta` for a morning item: the blind label of the side's first chapter. */
export async function morningMeta(item: string): Promise<{ title: string; album: string } | null> {
  const it = parseMorningItem(item);
  const view = it ? views.get(it.crate) : undefined;
  if (!it || !view) return null;
  const c = chapterAt(view, it.side, 0);
  return { title: c ? chapterTitle(view, c) : "Morning crate", album: sideLabel(view, it.side) };
}
