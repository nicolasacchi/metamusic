// Offline file store (C115.2): chunks in Cache Storage "mm-media-v1", metadata in IndexedDB "mm-offline".
// Shared by the page (download manager) and the service worker; uses only APIs both contexts have.
export const CACHE_NAME = "mm-media-v1";
export const DB_NAME = "mm-offline";

export interface FileMeta {
  sha: string;
  ext: string;
  content_type: string;
  bytes: number;
  chunks: number;
  complete: boolean;
  track_id: string;
  title: string;
  album: string;
  duration_s: number;
  cover_key: string | null;
  downloaded_at: number;
}

export interface TrackRow {
  track_id: string;
  /** "<sha>.<ext>" */
  file: string;
  /** "<key>.png" */
  cover: string | null;
}

export interface ChunkStore {
  getChunk(sha: string, ext: string, n: number): Promise<Uint8Array | null>;
  putChunk(sha: string, ext: string, n: number, data: Uint8Array): Promise<void>;
  deleteChunks(sha: string, ext: string): Promise<void>;
  meta(sha: string, ext: string): Promise<FileMeta | null>;
  putMeta(m: FileMeta): Promise<void>;
  deleteMeta(sha: string, ext: string): Promise<void>;
  listFiles(): Promise<FileMeta[]>;
  putTrack(t: TrackRow): Promise<void>;
  getTrack(trackId: string): Promise<TrackRow | null>;
  deleteTrack(trackId: string): Promise<void>;
  listTracks(): Promise<TrackRow[]>;
}

export const fileKey = (sha: string, ext: string) => `${sha}.${ext}`;
export const chunkKey = (sha: string, ext: string, n: number) => `/__offline/${sha}.${ext}/${n}`;

export function memoryStore(): ChunkStore {
  const chunks = new Map<string, Uint8Array>();
  const files = new Map<string, FileMeta>();
  const tracks = new Map<string, TrackRow>();
  return {
    async getChunk(sha, ext, n) {
      return chunks.get(chunkKey(sha, ext, n))?.slice() ?? null;
    },
    async putChunk(sha, ext, n, data) {
      chunks.set(chunkKey(sha, ext, n), data.slice());
    },
    async deleteChunks(sha, ext) {
      const prefix = `/__offline/${sha}.${ext}/`;
      for (const k of [...chunks.keys()]) if (k.startsWith(prefix)) chunks.delete(k);
    },
    async meta(sha, ext) {
      const m = files.get(fileKey(sha, ext));
      return m ? { ...m } : null;
    },
    async putMeta(m) {
      files.set(fileKey(m.sha, m.ext), { ...m });
    },
    async deleteMeta(sha, ext) {
      files.delete(fileKey(sha, ext));
    },
    async listFiles() {
      return [...files.values()].map((m) => ({ ...m }));
    },
    async putTrack(t) {
      tracks.set(t.track_id, { ...t });
    },
    async getTrack(id) {
      const t = tracks.get(id);
      return t ? { ...t } : null;
    },
    async deleteTrack(id) {
      tracks.delete(id);
    },
    async listTracks() {
      return [...tracks.values()].map((t) => ({ ...t }));
    },
  };
}

export interface StoreEnv {
  caches: CacheStorage;
  indexedDB: IDBFactory;
}

function req<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((res, rej) => {
    r.onsuccess = () => res(r.result);
    r.onerror = () => rej(r.error);
  });
}

function done(tx: IDBTransaction): Promise<void> {
  return new Promise((res, rej) => {
    tx.oncomplete = () => res();
    tx.onerror = () => rej(tx.error);
    tx.onabort = () => rej(tx.error);
  });
}

/** CacheStorage + IndexedDB implementation. Worker updates never touch either (C115.1). */
export function cacheStore(env: StoreEnv = { caches: globalThis.caches, indexedDB: globalThis.indexedDB }): ChunkStore {
  let dbp: Promise<IDBDatabase> | null = null;
  const db = () =>
    (dbp ??= new Promise<IDBDatabase>((res, rej) => {
      const o = env.indexedDB.open(DB_NAME, 1);
      o.onupgradeneeded = () => {
        o.result.createObjectStore("files");
        o.result.createObjectStore("tracks", { keyPath: "track_id" });
      };
      o.onsuccess = () => res(o.result);
      o.onerror = () => {
        dbp = null;
        rej(o.error);
      };
    }));
  const store = async (name: "files" | "tracks", mode: IDBTransactionMode) => {
    const tx = (await db()).transaction(name, mode);
    return { s: tx.objectStore(name), tx };
  };
  const cache = () => env.caches.open(CACHE_NAME);
  return {
    async getChunk(sha, ext, n) {
      const r = await (await cache()).match(chunkKey(sha, ext, n));
      return r ? new Uint8Array(await r.arrayBuffer()) : null;
    },
    async putChunk(sha, ext, n, data) {
      await (await cache()).put(chunkKey(sha, ext, n), new Response(data.slice()));
    },
    async deleteChunks(sha, ext) {
      const c = await cache();
      const prefix = `/__offline/${sha}.${ext}/`;
      for (const k of await c.keys()) if (new URL(k.url).pathname.startsWith(prefix)) await c.delete(k);
    },
    async meta(sha, ext) {
      const { s } = await store("files", "readonly");
      return ((await req(s.get(fileKey(sha, ext)))) as FileMeta | undefined) ?? null;
    },
    async putMeta(m) {
      const { s, tx } = await store("files", "readwrite");
      s.put(m, fileKey(m.sha, m.ext));
      await done(tx);
    },
    async deleteMeta(sha, ext) {
      const { s, tx } = await store("files", "readwrite");
      s.delete(fileKey(sha, ext));
      await done(tx);
    },
    async listFiles() {
      const { s } = await store("files", "readonly");
      return (await req(s.getAll())) as FileMeta[];
    },
    async putTrack(t) {
      const { s, tx } = await store("tracks", "readwrite");
      s.put(t);
      await done(tx);
    },
    async getTrack(id) {
      const { s } = await store("tracks", "readonly");
      return ((await req(s.get(id))) as TrackRow | undefined) ?? null;
    },
    async deleteTrack(id) {
      const { s, tx } = await store("tracks", "readwrite");
      s.delete(id);
      await done(tx);
    },
    async listTracks() {
      const { s } = await store("tracks", "readonly");
      return (await req(s.getAll())) as TrackRow[];
    },
  };
}
