// Offline download manager (C115.3). Network code, so it lives in src/api/. Chunks (2 MiB, Range requests against
// the signed link) go to the shared ChunkStore; the whole file's sha256 is verified before `complete` is set.
import { api } from "./client";
import type { components } from "./schema";
import { CHUNK, chunkCount } from "./sw/chunks";
import { cacheStore, fileKey, type ChunkStore, type FileMeta } from "./sw/store";

export type MediaLinks = components["schemas"]["MediaLinksOutBody"];
export type OfflineSet = components["schemas"]["OfflineSet"];
export type Format = "flac" | "mp3";

export type OfflineErrorCode = "quota" | "hash" | "length" | "http" | "aborted" | "links" | "unavailable";
export class OfflineError extends Error {
  readonly code: OfflineErrorCode;
  constructor(code: OfflineErrorCode, message: string) {
    super(message);
    this.name = "OfflineError";
    this.code = code;
  }
}

export interface MediaLink {
  /** Signed, relative /media/... URL. */
  url: string;
  bytes: number;
  sha256: string;
  content_type: string;
  ext: Format;
  /** Mints a fresh URL after a 403 (expired link); null when that fails. */
  remint?: () => Promise<string | null>;
}

export interface TrackMeta {
  /** Absent for a Morning Crate side (`morning` is set instead). */
  track_id?: string;
  /** A side of a Morning Crate: the file is stored without a `tracks` row. Its title must stay blind ("Morning crate"). */
  morning?: { id: string; side: number };
  title: string;
  album: string;
  duration_s: number;
  /** Signed 512 px cover URL (relative /media/<key>.png?...), if any. */
  cover_url?: string | null;
}

/** One side of a Morning Crate as `downloadMorning` takes it. */
export interface MorningSideFile {
  side: number;
  label: string;
  url: string;
  sha256: string;
  bytes: number;
  duration_s: number;
}

export interface StorageLike {
  estimate?(): Promise<{ quota?: number; usage?: number }>;
  persist?(): Promise<boolean>;
}

export interface OfflineDeps {
  store: ChunkStore;
  fetch(url: string, init?: RequestInit): Promise<Response>;
  sha256(data: Uint8Array): Promise<string>;
  storage?: StorageLike | null;
  mediaLinks(trackId: string): Promise<MediaLinks | null>;
  /** Throws on a network or API failure. */
  offlineSet(): Promise<OfflineSet>;
  deviceFormat(): Format;
  now(): number;
  sleep?(ms: number): Promise<void>;
}

export interface LocalItem {
  track_id: string;
  title: string;
  album: string;
  duration_s: number;
  format: Format;
  bytes: number;
  src: string;
  coverSrc: string | null;
  contentType: string;
}

export interface LocalSource {
  src: string;
  contentType: string;
  format: Format;
  coverSrc: string | null;
}

export type Status = "none" | "partial" | "complete";

export interface SyncSummary {
  downloaded: string[];
  failed: { track_id: string; error: string }[];
  evicted: string[];
  stopped: "quota" | "aborted" | null;
}

const RETRIES = 3;
const MAX_REMINTS = 2;
const QUOTA_MARGIN = 1.1;
const AUDIO: string[] = ["flac", "mp3"];

export function hex(buf: ArrayBuffer): string {
  return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function abortError(): OfflineError {
  return new OfflineError("aborted", "Download cancelled");
}

export function createOfflineManager(deps: OfflineDeps) {
  const sleep = deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  let persistTried = false;
  const mgr = {
    /** Result of the single storage.persist() call; null until the first successful download. */
    persisted: null as boolean | null,

    async downloadTrack(link: MediaLink, meta: TrackMeta, onProgress?: (done: number, total: number) => void, signal?: AbortSignal): Promise<void> {
      const { store } = deps;
      const sha = link.sha256;
      const ext = link.ext;
      const total = link.bytes;
      if (total <= 0) throw new OfflineError("length", "Empty media file");
      const existing = await store.meta(sha, ext);
      if (existing?.complete) {
        if (meta.morning) await store.putMeta({ ...existing, morning: meta.morning });
        else await mgr.attach(existing, meta as TrackMeta & { track_id: string }, null);
        return;
      }
      await mgr.checkQuota(total);
      const n = chunkCount(total);
      const m: FileMeta = {
        sha,
        ext,
        content_type: link.content_type,
        bytes: total,
        chunks: n,
        complete: false,
        track_id: meta.track_id ?? "",
        title: meta.title,
        album: meta.album,
        duration_s: meta.duration_s,
        cover_key: null,
        downloaded_at: 0,
        ...(meta.morning ? { morning: meta.morning } : {}),
      };
      await store.putMeta(m);
      const parts: Uint8Array[] = [];
      let done = 0;
      let url = link.url;
      let remints = 0;
      for (let i = 0; i < n; i++) {
        if (signal?.aborted) throw abortError();
        const want = Math.min(CHUNK, total - i * CHUNK);
        let data = await store.getChunk(sha, ext, i);
        if (!data || data.length !== want) {
          let attempt = 0;
          for (;;) {
            if (signal?.aborted) throw abortError();
            let status = -1;
            try {
              const res = await deps.fetch(url, { headers: { Range: `bytes=${i * CHUNK}-${i * CHUNK + want - 1}` }, signal, cache: "no-store" });
              status = res.status;
              if (res.status === 206 || (res.status === 200 && i === 0 && n === 1)) {
                const buf = new Uint8Array(await res.arrayBuffer());
                if (buf.length === want) {
                  data = buf;
                  break;
                }
                throw new OfflineError("length", "Unexpected chunk length");
              }
              if (res.status === 200) {
                const buf = new Uint8Array(await res.arrayBuffer());
                if (buf.length !== total) throw new OfflineError("length", "Unexpected file length");
                data = buf.slice(i * CHUNK, i * CHUNK + want);
                break;
              }
            } catch (e) {
              if (signal?.aborted || (e instanceof DOMException && e.name === "AbortError")) throw abortError();
              if (e instanceof OfflineError && e.code === "length") {
                await mgr.discard(m);
                throw e;
              }
              // network failure (status stays -1) or a transient read error: retry
            }
            if (status === 403 && link.remint && remints < MAX_REMINTS) {
              remints++;
              const fresh = await link.remint();
              if (!fresh) throw new OfflineError("links", "Could not refresh the download link");
              url = fresh;
              continue;
            }
            if (status >= 400 && status < 500 && status !== 429 && status !== 408) {
              throw new OfflineError("http", `Download failed (${status})`);
            }
            if (++attempt >= RETRIES) throw new OfflineError("http", status > 0 ? `Download failed (${status})` : "Network error during download");
            await sleep(250 * attempt);
          }
          await store.putChunk(sha, ext, i, data!);
        }
        parts.push(data!);
        done += data!.length;
        onProgress?.(done, total);
      }
      const all = new Uint8Array(total);
      let at = 0;
      for (const p of parts) {
        all.set(p, at);
        at += p.length;
      }
      if (at !== total) {
        await mgr.discard(m);
        throw new OfflineError("length", "Downloaded length does not match");
      }
      if ((await deps.sha256(all)) !== sha) {
        await mgr.discard(m);
        throw new OfflineError("hash", "Downloaded file failed verification");
      }
      const full: FileMeta = { ...m, complete: true, downloaded_at: deps.now() };
      await store.putMeta(full);
      if (!meta.morning) {
        const cover = await mgr.fetchCover(meta, signal);
        await mgr.attach(full, meta as TrackMeta & { track_id: string }, cover);
      }
      await mgr.persistOnce();
    },

    async attach(file: FileMeta, meta: TrackMeta & { track_id: string }, cover: string | null) {
      const { store } = deps;
      const prev = await store.getTrack(meta.track_id);
      const row = { track_id: meta.track_id, file: fileKey(file.sha, file.ext), cover: cover ?? prev?.cover ?? null };
      await store.putTrack(row);
      if (prev && prev.file !== row.file) await mgr.dropFile(prev.file, meta.track_id);
      if (prev?.cover && prev.cover !== row.cover) await mgr.dropFile(prev.cover, meta.track_id);
    },

    async dropFile(key: string, exceptTrack: string) {
      const { store } = deps;
      for (const t of await store.listTracks()) if (t.track_id !== exceptTrack && (t.file === key || t.cover === key)) return;
      const dot = key.lastIndexOf(".");
      const sha = key.slice(0, dot);
      const ext = key.slice(dot + 1);
      await store.deleteChunks(sha, ext);
      await store.deleteMeta(sha, ext);
    },

    async discard(m: FileMeta) {
      await deps.store.deleteChunks(m.sha, m.ext);
      await deps.store.deleteMeta(m.sha, m.ext);
    },

    async fetchCover(meta: TrackMeta, signal?: AbortSignal): Promise<string | null> {
      if (!meta.cover_url) return null;
      const key = /\/media\/([0-9a-f]+)\.png(?:\?|$)/.exec(meta.cover_url)?.[1];
      if (!key) return null;
      try {
        const have = await deps.store.meta(key, "png");
        if (have?.complete) return `${key}.png`;
        const res = await deps.fetch(meta.cover_url, { signal, cache: "no-store" });
        if (!res.ok) return null;
        const data = new Uint8Array(await res.arrayBuffer());
        await deps.store.putChunk(key, "png", 0, data);
        await deps.store.putMeta({
          sha: key,
          ext: "png",
          content_type: "image/png",
          bytes: data.length,
          chunks: 1,
          complete: true,
          track_id: meta.track_id ?? "",
          title: meta.title,
          album: meta.album,
          duration_s: meta.duration_s,
          cover_key: key,
          downloaded_at: deps.now(),
        });
        return `${key}.png`;
      } catch {
        return null; // a missing cover never fails a download
      }
    },

    async checkQuota(bytes: number) {
      const est = await deps.storage?.estimate?.().catch(() => undefined);
      if (!est || est.quota === undefined) return;
      const free = est.quota - (est.usage ?? 0);
      if (free < bytes * QUOTA_MARGIN) throw new OfflineError("quota", "Not enough free storage on this device");
    },

    async persistOnce() {
      if (persistTried || !deps.storage?.persist) return;
      persistTried = true;
      try {
        mgr.persisted = await deps.storage.persist();
      } catch {
        mgr.persisted = false;
      }
    },

    async status(trackId: string): Promise<Status> {
      const row = await deps.store.getTrack(trackId);
      if (row) {
        const dot = row.file.lastIndexOf(".");
        const m = await deps.store.meta(row.file.slice(0, dot), row.file.slice(dot + 1));
        if (m?.complete) return "complete";
      }
      const files = await deps.store.listFiles();
      return files.some((f) => f.track_id === trackId && AUDIO.includes(f.ext)) ? "partial" : "none";
    },

    async localSource(trackId: string): Promise<LocalSource | null> {
      const row = await deps.store.getTrack(trackId);
      if (!row) return null;
      const dot = row.file.lastIndexOf(".");
      const sha = row.file.slice(0, dot);
      const ext = row.file.slice(dot + 1);
      const m = await deps.store.meta(sha, ext);
      if (!m?.complete) return null;
      return { src: `/media/${sha}.${ext}`, contentType: m.content_type, format: ext as Format, coverSrc: row.cover ? `/media/${row.cover}` : null };
    },

    /** Everything the offline list and player need, from the local stores only. */
    async list(): Promise<LocalItem[]> {
      const [rows, files] = await Promise.all([deps.store.listTracks(), deps.store.listFiles()]);
      const out: LocalItem[] = [];
      for (const r of rows) {
        const f = files.find((x) => fileKey(x.sha, x.ext) === r.file);
        if (!f?.complete) continue;
        out.push({
          track_id: r.track_id,
          title: f.title,
          album: f.album,
          duration_s: f.duration_s,
          format: f.ext as Format,
          bytes: f.bytes,
          src: `/media/${f.sha}.${f.ext}`,
          coverSrc: r.cover ? `/media/${r.cover}` : null,
          contentType: f.content_type,
        });
      }
      return out.sort((a, b) => a.title.localeCompare(b.title));
    },

    async evict(trackId: string): Promise<void> {
      const { store } = deps;
      const row = await store.getTrack(trackId);
      const keys = new Set<string>();
      if (row) {
        keys.add(row.file);
        if (row.cover) keys.add(row.cover);
      }
      for (const f of await store.listFiles()) if (f.track_id === trackId) keys.add(fileKey(f.sha, f.ext));
      await store.deleteTrack(trackId);
      for (const k of keys) await mgr.dropFile(k, trackId);
    },

    /**
     * Downloads the sides of a Morning Crate (C384.5): each file is stored without a `tracks` row under neutral names
     * ("Morning crate" / "Side A"), so no private title is cached before the reveal. When every side is in, the sides of
     * any other crate are evicted: the phone keeps the newest crate only. A failure keeps the previous crate.
     */
    async downloadMorning(crateId: string, sides: MorningSideFile[], onProgress?: (side: number, done: number, total: number) => void, signal?: AbortSignal): Promise<void> {
      for (const s of sides) {
        await mgr.downloadTrack(
          { url: s.url, bytes: s.bytes, sha256: s.sha256, content_type: "audio/flac", ext: "flac" },
          { morning: { id: crateId, side: s.side }, title: "Morning crate", album: s.label, duration_s: s.duration_s },
          (d, t) => onProgress?.(s.side, d, t),
          signal,
        );
      }
      await mgr.evictMorningExcept(crateId);
    },

    /** Removes the sides of every Morning Crate except `keep` (all of them when `keep` is null). Returns the evicted crate ids. */
    async evictMorningExcept(keep: string | null): Promise<string[]> {
      const { store } = deps;
      const gone = new Set<string>();
      for (const f of await store.listFiles()) {
        if (!f.morning || f.morning.id === keep) continue;
        gone.add(f.morning.id);
        await mgr.discard(f);
      }
      return [...gone].sort();
    },

    /** The complete local file of one side of a Morning Crate, or null. */
    async morningSource(crateId: string, side: number): Promise<LocalSource | null> {
      for (const f of await deps.store.listFiles()) {
        if (f.complete && f.morning?.id === crateId && f.morning.side === side) return { src: `/media/${f.sha}.${f.ext}`, contentType: f.content_type, format: f.ext as Format, coverSrc: null };
      }
      return null;
    },

    /** Sides of one crate that are complete on this device. */
    async morningSides(crateId: string): Promise<number[]> {
      return (await deps.store.listFiles()).filter((f) => f.complete && f.morning?.id === crateId).map((f) => f.morning!.side).sort((a, b) => a - b);
    },

    /** Downloads one track by id: links from get_media_links, device format (C115.3). */
    async downloadById(trackId: string, meta: Omit<TrackMeta, "track_id" | "cover_url">, onProgress?: (done: number, total: number) => void, signal?: AbortSignal) {
      const links = await deps.mediaLinks(trackId);
      if (!links) throw new OfflineError("links", "Could not load media links");
      const useFlac = links.flac !== null && deps.deviceFormat() === "flac";
      const f = useFlac ? links.flac! : links.mp3;
      const ext: Format = useFlac ? "flac" : "mp3";
      // a Listener's links carry no hash (D-U1-17); the offline set is Owner-only (D-U1-10), so a link without one is refused
      if (!f.sha256) throw new OfflineError("links", "The media links carry no hash");
      const sha256 = f.sha256;
      const remint = async () => {
        const fresh = await deps.mediaLinks(trackId);
        return (ext === "flac" ? fresh?.flac?.url : fresh?.mp3.url) ?? null;
      };
      await mgr.downloadTrack(
        { url: f.url, bytes: f.bytes, sha256, content_type: f.content_type, ext, remint },
        { ...meta, track_id: trackId, cover_url: links.cover?.url_512 ?? null },
        onProgress,
        signal,
      );
    },

    /** get_offline_set, download what is missing in the device format, then evict what left the set. */
    async sync(onProgress?: (trackId: string, done: number, total: number) => void, signal?: AbortSignal): Promise<SyncSummary> {
      const set = await deps.offlineSet();
      const summary: SyncSummary = { downloaded: [], failed: [], evicted: [], stopped: null };
      const items = [...set.items].sort((a, b) => a.position - b.position);
      for (const it of items) {
        if ((await mgr.status(it.track_id)) === "complete") continue;
        try {
          await mgr.downloadById(it.track_id, { title: it.title, album: it.album, duration_s: it.duration_s }, (d, t) => onProgress?.(it.track_id, d, t), signal);
          summary.downloaded.push(it.track_id);
        } catch (e) {
          if (e instanceof OfflineError && (e.code === "quota" || e.code === "aborted")) {
            summary.stopped = e.code;
            break;
          }
          summary.failed.push({ track_id: it.track_id, error: e instanceof Error ? e.message : String(e) });
        }
      }
      const keep = new Set(items.map((i) => i.track_id));
      const local = new Set<string>();
      for (const r of await deps.store.listTracks()) local.add(r.track_id);
      for (const f of await deps.store.listFiles()) if (!f.morning) local.add(f.track_id);
      for (const id of [...local].sort()) {
        if (keep.has(id)) continue;
        await mgr.evict(id);
        summary.evicted.push(id);
      }
      return summary;
    },
  };
  return mgr;
}

export type OfflineManager = ReturnType<typeof createOfflineManager>;

/** True for a failed fetch (offline, DNS, refused), as opposed to an HTTP answer. */
export function isNetworkError(e: unknown): boolean {
  return e instanceof TypeError || (e instanceof DOMException && e.name === "NetworkError");
}

export function realDeps(deviceFormat: () => Format): OfflineDeps {
  return {
    store: cacheStore(),
    fetch: (url, init) => globalThis.fetch(url, init),
    sha256: async (data) => hex(await crypto.subtle.digest("SHA-256", data as Uint8Array<ArrayBuffer>)),
    storage: typeof navigator !== "undefined" ? navigator.storage : null,
    mediaLinks: async (id) => (await api.GET("/api/tracks/{id}/media", { params: { path: { id } } })).data ?? null,
    offlineSet: async () => {
      const r = await api.GET("/api/offline-set");
      if (!r.data) throw new OfflineError("unavailable", "Could not load the offline set");
      return r.data;
    },
    deviceFormat,
    now: () => Date.now(),
  };
}
