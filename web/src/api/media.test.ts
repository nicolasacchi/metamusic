import { describe, expect, test } from "bun:test";
import { ClipGone, ClipLinkExpired, fetchClip } from "./media";

const origin = "https://mm.example.com";
const key = "clip-key-placeholder";

function fakeFetch(status: number) {
  const calls: { url: string; init: RequestInit | undefined }[] = [];
  const f = async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    return new Response(status === 200 ? new Uint8Array([1, 2, 3]) : "x", { status });
  };
  return { f, calls };
}

describe("fetchClip", () => {
  test("refuses non-/media/ and cross-origin URLs without fetching", async () => {
    const { f, calls } = fakeFetch(200);
    const opts = { fetch: f, origin };
    for (const u of [
      "/api/tracks/x",
      `https://evil.example.org/media/${key}.flac`,
      `//evil.example.org/media/${key}.flac`,
      `http://mm.example.com/media/${key}.flac`,
      "/media/../api/x",
      "javascript:alert(1)",
      "",
    ]) {
      await expect(fetchClip(u, undefined, opts)).rejects.toThrow();
    }
    expect(calls).toHaveLength(0);
  });
  test("passes force-cache and the signal; returns bytes", async () => {
    const { f, calls } = fakeFetch(200);
    const ac = new AbortController();
    const buf = await fetchClip(`/media/${key}.flac?exp=1&sig=z`, ac.signal, { fetch: f, origin });
    expect(new Uint8Array(buf)).toEqual(new Uint8Array([1, 2, 3]));
    expect(calls[0]!.init?.cache).toBe("force-cache");
    expect(calls[0]!.init?.signal).toBe(ac.signal);
    expect(calls[0]!.url).toBe(`${origin}/media/${key}.flac?exp=1&sig=z`);
  });
  test("403 is ClipLinkExpired, 404 is ClipGone", async () => {
    await expect(fetchClip(`/media/${key}.flac`, undefined, { fetch: fakeFetch(403).f, origin })).rejects.toBeInstanceOf(ClipLinkExpired);
    await expect(fetchClip(`/media/${key}.flac`, undefined, { fetch: fakeFetch(404).f, origin })).rejects.toBeInstanceOf(ClipGone);
  });
  test("other statuses are plain errors", async () => {
    const e = await fetchClip(`/media/${key}.flac`, undefined, { fetch: fakeFetch(500).f, origin }).catch((x: unknown) => x);
    expect(e).toBeInstanceOf(Error);
    expect(e).not.toBeInstanceOf(ClipGone);
    expect(e).not.toBeInstanceOf(ClipLinkExpired);
  });
});
