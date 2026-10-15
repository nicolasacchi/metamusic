import { describe, expect, test } from "bun:test";
import { CHUNK } from "./chunks";
import { serveMedia } from "./respond";
import { memoryStore, type FileMeta } from "./store";

const MiB = 1024 * 1024;
const sha = "b".repeat(64);
const size = 9 * MiB + 17;
const source = new Uint8Array(size).map((_, i) => (i * 13 + 5) & 0xff);

async function seeded(complete = true) {
  const s = memoryStore();
  const n = Math.ceil(size / CHUNK);
  for (let i = 0; i < n; i++) await s.putChunk(sha, "flac", i, source.slice(i * CHUNK, Math.min(size, (i + 1) * CHUNK)));
  const m: FileMeta = {
    sha,
    ext: "flac",
    content_type: "audio/flac",
    bytes: size,
    chunks: n,
    complete,
    track_id: "t",
    title: "x",
    album: "",
    duration_s: 1,
    cover_key: null,
    downloaded_at: 0,
  };
  await s.putMeta(m);
  return s;
}
const req = (range?: string) =>
  new Request(`https://mm.example.com/media/${sha}.flac?exp=1`, range ? { headers: { Range: range } } : {});

describe("serveMedia", () => {
  test("no Range: 200 with the full length, streamed", async () => {
    const r = await serveMedia(req(), await seeded());
    expect(r?.status).toBe(200);
    expect(r?.headers.get("Content-Length")).toBe(String(size));
    expect(r?.headers.get("Content-Type")).toBe("audio/flac");
    expect(r?.headers.get("Accept-Ranges")).toBe("bytes");
    expect(r?.headers.get("ETag")).toBe(`"${sha}"`);
    expect(new Uint8Array(await r!.arrayBuffer())).toEqual(source);
  });
  test("bytes=a-b: 206 with Content-Range", async () => {
    const a = 2 * MiB - 5;
    const r = await serveMedia(req(`bytes=${a}-${a + 9}`), await seeded());
    expect(r?.status).toBe(206);
    expect(r?.headers.get("Content-Range")).toBe(`bytes ${a}-${a + 9}/${size}`);
    expect(r?.headers.get("Content-Length")).toBe("10");
    expect(new Uint8Array(await r!.arrayBuffer())).toEqual(source.slice(a, a + 10));
  });
  test("open ranges are capped at 4 MiB", async () => {
    const r = await serveMedia(req("bytes=0-"), await seeded());
    expect(r?.status).toBe(206);
    expect(r?.headers.get("Content-Range")).toBe(`bytes 0-${4 * MiB - 1}/${size}`);
    expect((await r!.arrayBuffer()).byteLength).toBe(4 * MiB);
  });
  test("suffix range", async () => {
    const r = await serveMedia(req("bytes=-100"), await seeded());
    expect(r?.headers.get("Content-Range")).toBe(`bytes ${size - 100}-${size - 1}/${size}`);
    expect(new Uint8Array(await r!.arrayBuffer())).toEqual(source.slice(size - 100));
  });
  test("unsatisfiable: 416 bytes */size", async () => {
    const r = await serveMedia(req(`bytes=${size}-`), await seeded());
    expect(r?.status).toBe(416);
    expect(r?.headers.get("Content-Range")).toBe(`bytes */${size}`);
  });
  test("incomplete or unknown file: null (pass to the network)", async () => {
    expect(await serveMedia(req(), await seeded(false))).toBeNull();
    expect(await serveMedia(req(), memoryStore())).toBeNull();
  });
  test("a missing chunk falls back to the network", async () => {
    const s = await seeded();
    await s.deleteChunks(sha, "flac");
    const r = await serveMedia(req("bytes=0-9"), s);
    expect(r).toBeNull();
  });
});
