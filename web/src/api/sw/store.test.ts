import { describe, expect, test } from "bun:test";
import { cacheStore, memoryStore, chunkKey, fileKey, type FileMeta } from "./store";

const meta = (over: Partial<FileMeta> = {}): FileMeta => ({
  sha: "a".repeat(64),
  ext: "flac",
  content_type: "audio/flac",
  bytes: 10,
  chunks: 1,
  complete: false,
  track_id: "t1",
  title: "Synthetic",
  album: "Album",
  duration_s: 12,
  cover_key: null,
  downloaded_at: 0,
  ...over,
});

describe("keys", () => {
  test("chunk keys follow the C115.2 layout", () => {
    expect(chunkKey("abc", "flac", 3)).toBe("/__offline/abc.flac/3");
    expect(fileKey("abc", "mp3")).toBe("abc.mp3");
  });
});

describe("memoryStore", () => {
  test("chunk round trip and delete", async () => {
    const s = memoryStore();
    await s.putChunk("abc", "flac", 0, new Uint8Array([1, 2]));
    await s.putChunk("abc", "flac", 1, new Uint8Array([3]));
    expect(await s.getChunk("abc", "flac", 1)).toEqual(new Uint8Array([3]));
    expect(await s.getChunk("abc", "flac", 2)).toBeNull();
    await s.deleteChunks("abc", "flac");
    expect(await s.getChunk("abc", "flac", 0)).toBeNull();
  });
  test("meta, files list and tracks", async () => {
    const s = memoryStore();
    const m = meta();
    await s.putMeta(m);
    expect((await s.meta(m.sha, m.ext))?.title).toBe("Synthetic");
    expect(await s.listFiles()).toHaveLength(1);
    await s.putTrack({ track_id: "t1", file: fileKey(m.sha, m.ext), cover: null });
    expect(await s.getTrack("t1")).toEqual({ track_id: "t1", file: "a".repeat(64) + ".flac", cover: null });
    expect(await s.listTracks()).toHaveLength(1);
    await s.deleteTrack("t1");
    await s.deleteMeta(m.sha, m.ext);
    expect(await s.meta(m.sha, m.ext)).toBeNull();
    expect(await s.getTrack("t1")).toBeNull();
  });
  test("stores copies, not references", async () => {
    const s = memoryStore();
    const data = new Uint8Array([9]);
    await s.putChunk("abc", "mp3", 0, data);
    data[0] = 0;
    expect((await s.getChunk("abc", "mp3", 0))?.[0]).toBe(9);
  });
});

describe("cacheStore chunk layer", () => {
  // A Map-backed CacheStorage double: enough for chunk put/get/delete (IndexedDB is covered by the Playwright spec).
  function fakeCaches() {
    const opened: string[] = [];
    const entries = new Map<string, Uint8Array>();
    const cache = {
      async put(k: string, r: Response) {
        entries.set(new URL(k, "https://mm.example.com").pathname, new Uint8Array(await r.arrayBuffer()));
      },
      async match(k: string) {
        const v = entries.get(new URL(k, "https://mm.example.com").pathname);
        return v ? new Response(v.slice()) : undefined;
      },
      async keys() {
        return [...entries.keys()].map((p) => new Request(new URL(p, "https://mm.example.com")));
      },
      async delete(k: Request) {
        return entries.delete(new URL(k.url).pathname);
      },
    };
    const caches = {
      async open(name: string) {
        opened.push(name);
        return cache;
      },
    } as unknown as CacheStorage;
    return { caches, opened, entries };
  }
  test("uses cache mm-media-v1 and the /__offline/<sha>.<ext>/<n> keys", async () => {
    const f = fakeCaches();
    const s = cacheStore({ caches: f.caches, indexedDB: undefined as unknown as IDBFactory });
    await s.putChunk("abc", "flac", 0, new Uint8Array([1, 2, 3]));
    await s.putChunk("abc", "flac", 1, new Uint8Array([4]));
    await s.putChunk("abc", "mp3", 0, new Uint8Array([5]));
    expect([...f.entries.keys()].sort()).toEqual(["/__offline/abc.flac/0", "/__offline/abc.flac/1", "/__offline/abc.mp3/0"]);
    expect(new Set(f.opened)).toEqual(new Set(["mm-media-v1"]));
    expect(await s.getChunk("abc", "flac", 0)).toEqual(new Uint8Array([1, 2, 3]));
    expect(await s.getChunk("abc", "flac", 9)).toBeNull();
    await s.deleteChunks("abc", "flac");
    expect([...f.entries.keys()]).toEqual(["/__offline/abc.mp3/0"]);
  });
});
