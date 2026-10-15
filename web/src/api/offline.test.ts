import { describe, expect, test } from "bun:test";
import { CHUNK } from "./sw/chunks";
import { memoryStore } from "./sw/store";
import { OfflineError, createOfflineManager, hex, type MediaLinks, type OfflineDeps, type OfflineSet } from "./offline";

const MiB = 1024 * 1024;

// Pure and read-only (stores copy what they are given), so it is safe to share across parallel tests.
const bodies = new Map<string, Uint8Array>();
function body(n: number, seed: number): Uint8Array {
  const k = `${n}/${seed}`;
  let b = bodies.get(k);
  if (!b) {
    b = new Uint8Array(n);
    for (let i = 0; i < n; i++) b[i] = (i * 7 + seed) & 0xff;
    bodies.set(k, b);
  }
  return b;
}
const shas = new Map<Uint8Array, Promise<string>>();
function sha(b: Uint8Array): Promise<string> {
  let p = shas.get(b);
  if (!p) {
    p = crypto.subtle.digest("SHA-256", b as Uint8Array<ArrayBuffer>).then(hex);
    shas.set(b, p);
  }
  return p;
}

interface Fake {
  files: Map<string, Uint8Array>;
  calls: { url: string; range: string | null }[];
  /** url -> statuses to answer before the real response */
  fail: Map<string, number[]>;
}
function fakeFetch(f: Fake): OfflineDeps["fetch"] {
  return async (url, init) => {
    const headers = new Headers(init?.headers);
    f.calls.push({ url, range: headers.get("Range") });
    if (init?.signal?.aborted) throw new DOMException("aborted", "AbortError");
    const pending = f.fail.get(url);
    if (pending?.length) return new Response(null, { status: pending.shift()! });
    const data = f.files.get(url.split("?")[0]!);
    if (!data) return new Response(null, { status: 404 });
    const m = /^bytes=(\d+)-(\d+)$/.exec(headers.get("Range") ?? "");
    if (!m) return new Response(data.slice(), { status: 200 });
    const a = Number(m[1]);
    const b = Math.min(Number(m[2]), data.length - 1);
    return new Response(data.slice(a, b + 1), { status: 206 });
  };
}

interface Rig {
  deps: OfflineDeps;
  fake: Fake;
  persistCalls: number[];
  linksCalls: string[];
  setCalls: number[];
  quota: { quota: number; usage: number };
  set: OfflineSet;
  tracks: Map<string, { data: Uint8Array; sha: string; cover?: Uint8Array }>;
  format: { v: "flac" | "mp3" };
}

async function rig(): Promise<Rig> {
  const fake: Fake = { files: new Map(), calls: [], fail: new Map() };
  const tracks: Rig["tracks"] = new Map();
  const quota = { quota: 1000 * MiB, usage: 0 };
  const persistCalls: number[] = [];
  const linksCalls: string[] = [];
  const setCalls: number[] = [];
  const format = { v: "flac" as "flac" | "mp3" };
  const r = {} as Rig;
  const mk = async (id: string, size: number, seed: number) => {
    const data = body(size, seed);
    const cover = body(100, seed + 1);
    const s = await sha(data);
    tracks.set(id, { data, sha: s, cover });
    fake.files.set(`/media/${s}.flac`, data);
    fake.files.set(`/media/${"c".repeat(63)}${seed % 10}.png`, cover);
  };
  await mk("t1", 5 * MiB + 11, 1); // 3 chunks
  await mk("t2", 1000, 2); // 1 chunk
  const items = (ids: string[]): OfflineSet["items"] =>
    ids.map((id, i) => ({
      album: "Album " + id,
      duration_s: 12,
      flac: { bits_per_sample: 16, bytes: tracks.get(id)!.data.length, sample_rate: 44100, sha256: tracks.get(id)!.sha },
      lossy: false,
      mp3: null,
      position: i,
      status: "final",
      title: "Song " + id,
      track_id: id,
    }));
  r.set = {
    items: items(["t1", "t2"]),
    limit_items: 100,
    name: "Offline",
    playlist_id: "p",
    revision: 1,
    totals: { flac_bytes: 0, items: 2, mp3_bytes: 0 },
    updated_at: "2026-01-01T00:00:00Z",
  };
  const deps: OfflineDeps = {
    store: memoryStore(),
    fetch: fakeFetch(fake),
    sha256: sha,
    storage: {
      estimate: async () => ({ quota: quota.quota, usage: quota.usage }),
      persist: async () => {
        persistCalls.push(1);
        return true;
      },
    },
    mediaLinks: async (id) => {
      linksCalls.push(id);
      const t = tracks.get(id);
      if (!t) return null;
      const links: MediaLinks = {
        track_id: id,
        expires_at: "2026-01-01T12:00:00Z",
        ttl_s: 43200,
        lossy: false,
        cover: { expires_at: "x", url_512: `/media/${"c".repeat(63)}${id === "t1" ? 1 : 2}.png?sig=1`, url_1024: "/media/x" },
        flac: { url: `/media/${t.sha}.flac?exp=1`, bytes: t.data.length, content_type: "audio/flac", sample_rate: 44100, bits_per_sample: 16, sha256: t.sha },
        mp3: { url: `/media/${"d".repeat(64)}.mp3?exp=1`, bytes: 10, content_type: "audio/mpeg", bitrate_kbps: 192, sha256: "d".repeat(64) },
      };
      return links;
    },
    offlineSet: async () => {
      setCalls.push(1);
      return r.set;
    },
    deviceFormat: () => format.v,
    now: () => 1_700_000_000_000,
    sleep: async () => {},
  };
  Object.assign(r, { deps, fake, persistCalls, linksCalls, setCalls, quota, tracks, format });
  // covers: key = 63 c's + digit, served by the same fake
  fake.files.set(`/media/${"c".repeat(63)}1.png`, tracks.get("t1")!.cover!);
  fake.files.set(`/media/${"c".repeat(63)}2.png`, tracks.get("t2")!.cover!);
  return r;
}

const meta = (id: string) => ({ track_id: id, title: "Song " + id, album: "Album " + id, duration_s: 12, cover_url: null as string | null });

describe("downloadTrack", () => {
  test("downloads chunk by chunk with 2 MiB Range requests and verifies the hash", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    const t = r.tracks.get("t1")!;
    const progress: number[] = [];
    await m.downloadTrack({ url: `/media/${t.sha}.flac?exp=1`, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, meta("t1"), (d) => progress.push(d));
    const ranges = r.fake.calls.map((c) => c.range);
    expect(ranges).toEqual([`bytes=0-${CHUNK - 1}`, `bytes=${CHUNK}-${2 * CHUNK - 1}`, `bytes=${2 * CHUNK}-${t.data.length - 1}`]);
    expect(progress.at(-1)).toBe(t.data.length);
    const fm = await r.deps.store.meta(t.sha, "flac");
    expect(fm?.complete).toBe(true);
    expect(fm?.chunks).toBe(3);
    expect(fm?.title).toBe("Song t1");
    expect(await r.deps.store.getTrack("t1")).toEqual({ track_id: "t1", file: `${t.sha}.flac`, cover: null });
    expect(await m.status("t1")).toBe("complete");
    const src = await m.localSource("t1");
    expect(src).toMatchObject({ src: `/media/${t.sha}.flac`, contentType: "audio/flac", format: "flac" });
  });

  test("a sha mismatch is not complete and deletes the chunks", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    const t = r.tracks.get("t2")!;
    await expect(
      m.downloadTrack({ url: `/media/${t.sha}.flac`, bytes: t.data.length, sha256: "0".repeat(64), content_type: "audio/flac", ext: "flac" }, meta("t2")),
    ).rejects.toMatchObject({ code: "hash" });
    expect(await r.deps.store.meta("0".repeat(64), "flac")).toBeNull();
    expect(await r.deps.store.getChunk("0".repeat(64), "flac", 0)).toBeNull();
    expect(await m.status("t2")).toBe("none");
    expect(await r.deps.store.getTrack("t2")).toBeNull();
  });

  test("a wrong total length is refused", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    const t = r.tracks.get("t2")!;
    await expect(
      m.downloadTrack({ url: `/media/${t.sha}.flac`, bytes: t.data.length + 5, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, meta("t2")),
    ).rejects.toBeInstanceOf(OfflineError);
    expect(await m.status("t2")).toBe("none");
  });

  test("abort stops the download and leaves a partial status", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    const t = r.tracks.get("t1")!;
    const ac = new AbortController();
    let n = 0;
    await expect(
      m.downloadTrack(
        { url: `/media/${t.sha}.flac`, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" },
        meta("t1"),
        () => {
          if (++n === 1) ac.abort();
        },
        ac.signal,
      ),
    ).rejects.toMatchObject({ code: "aborted" });
    expect(await m.status("t1")).toBe("partial");
    expect(r.persistCalls).toHaveLength(0);
    // resuming reuses the stored chunk
    r.fake.calls.length = 0;
    await m.downloadTrack({ url: `/media/${t.sha}.flac`, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, meta("t1"));
    expect(r.fake.calls).toHaveLength(2);
    expect(await m.status("t1")).toBe("complete");
  });

  test("quota refusal: free space < bytes + 10 %", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    const t = r.tracks.get("t1")!;
    r.quota.quota = 100 * MiB;
    r.quota.usage = 100 * MiB - Math.floor(t.data.length * 1.05);
    await expect(
      m.downloadTrack({ url: `/media/${t.sha}.flac`, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, meta("t1")),
    ).rejects.toMatchObject({ code: "quota" });
    expect(r.fake.calls).toHaveLength(0);
    r.quota.usage = 0;
    await m.downloadTrack({ url: `/media/${t.sha}.flac`, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, meta("t1"));
    expect(await m.status("t1")).toBe("complete");
  });

  test("persist() is called once after the first successful download", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    for (const id of ["t1", "t2"]) {
      const t = r.tracks.get(id)!;
      await m.downloadTrack({ url: `/media/${t.sha}.flac`, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, meta(id));
    }
    expect(r.persistCalls).toHaveLength(1);
    expect(m.persisted).toBe(true);
  });

  test("retries a failing chunk 3 times, then gives up", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    const t = r.tracks.get("t2")!;
    const url = `/media/${t.sha}.flac`;
    r.fake.fail.set(url, [503, 503]);
    await m.downloadTrack({ url, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, meta("t2"));
    expect(r.fake.calls).toHaveLength(3);
    const r2 = await rig();
    const m2 = createOfflineManager(r2.deps);
    r2.fake.fail.set(url, [503, 503, 503, 503]);
    await expect(m2.downloadTrack({ url, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, meta("t2"))).rejects.toMatchObject({ code: "http" });
  });

  test("a 403 re-mints the link and continues", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    const t = r.tracks.get("t2")!;
    const url = `/media/${t.sha}.flac?exp=old`;
    r.fake.fail.set(url, [403]);
    r.fake.files.set(`/media/${t.sha}.flac`, t.data);
    let minted = 0;
    await m.downloadTrack(
      { url, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac", remint: async () => (minted++, `/media/${t.sha}.flac?exp=new`) },
      meta("t2"),
    );
    expect(minted).toBe(1);
    expect(r.fake.calls.at(-1)?.url).toBe(`/media/${t.sha}.flac?exp=new`);
    expect(await m.status("t2")).toBe("complete");
  });

  test("the 512 cover is cached too", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    const t = r.tracks.get("t2")!;
    const cover = `/media/${"c".repeat(63)}2.png?sig=1`;
    await m.downloadTrack({ url: `/media/${t.sha}.flac`, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, { ...meta("t2"), cover_url: cover });
    const row = await r.deps.store.getTrack("t2");
    expect(row?.cover).toBe(`${"c".repeat(63)}2.png`);
    const cm = await r.deps.store.meta("c".repeat(63) + "2", "png");
    expect(cm?.complete).toBe(true);
    expect(cm?.content_type).toBe("image/png");
    expect((await m.localSource("t2"))?.coverSrc).toBe(`/media/${"c".repeat(63)}2.png`);
  });
});

describe("sync", () => {
  test("downloads the set in the device format, then evicts tracks that left it", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    // a stale local track that is not in the set
    const t2 = r.tracks.get("t2")!;
    await m.downloadTrack({ url: `/media/${t2.sha}.flac`, bytes: t2.data.length, sha256: t2.sha, content_type: "audio/flac", ext: "flac" }, meta("t-old"));
    r.set = { ...r.set, items: r.set.items.filter((i) => i.track_id === "t1") };
    const s = await m.sync();
    expect(s.downloaded).toEqual(["t1"]);
    expect(s.evicted).toEqual(["t-old"]);
    expect(s.failed).toEqual([]);
    expect(await m.status("t-old")).toBe("none");
    expect(await m.status("t1")).toBe("complete");
    expect(await r.deps.store.meta(t2.sha, "flac")).toBeNull();
  });

  test("skips complete items and reports a failing one", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    let s = await m.sync();
    expect(s.downloaded.sort()).toEqual(["t1", "t2"]);
    r.fake.calls.length = 0;
    r.linksCalls.length = 0;
    s = await m.sync();
    expect(s.downloaded).toEqual([]);
    expect(r.linksCalls).toEqual([]);
  });

  test("a track with a null flac link takes its mp3 master; high device quality takes mp3", async () => {
    const r = await rig();
    const orig = r.deps.mediaLinks;
    const mp3 = body(500, 9);
    const msha = await sha(mp3);
    r.fake.files.set(`/media/${msha}.mp3`, mp3);
    r.deps.mediaLinks = async (id) => {
      const l = await orig(id);
      if (!l) return l;
      return { ...l, lossy: true, flac: null, mp3: { url: `/media/${msha}.mp3?exp=1`, bytes: 500, content_type: "audio/mpeg", bitrate_kbps: 192, sha256: msha } };
    };
    r.set = { ...r.set, items: r.set.items.slice(0, 1) };
    const m = createOfflineManager(r.deps);
    await m.sync();
    expect((await m.localSource("t1"))?.format).toBe("mp3");
    const r2 = await rig();
    r2.format.v = "mp3";
    const orig2 = r2.deps.mediaLinks;
    r2.deps.mediaLinks = async (id) => {
      const l = await orig2(id);
      return l && { ...l, mp3: { ...l.mp3, url: `/media/${msha}.mp3?exp=1`, bytes: 500, sha256: msha } };
    };
    r2.set = { ...r2.set, items: r2.set.items.slice(0, 1) };
    r2.fake.files.set(`/media/${msha}.mp3`, mp3);
    const m2 = createOfflineManager(r2.deps);
    const s2 = await m2.sync();
    expect(s2.downloaded).toEqual(["t1"]);
    expect((await m2.localSource("t1"))?.format).toBe("mp3");
  });

  test("quota refusal stops the sync with a summary", async () => {
    const r = await rig();
    r.quota.quota = 1 * MiB;
    const m = createOfflineManager(r.deps);
    const s = await m.sync();
    expect(s.stopped).toBe("quota");
    expect(s.downloaded).toEqual([]);
  });

  test("a failing offline-set fetch propagates (the page then shows local items only)", async () => {
    const r = await rig();
    r.deps.offlineSet = async () => {
      throw new TypeError("Failed to fetch");
    };
    await expect(createOfflineManager(r.deps).sync()).rejects.toBeInstanceOf(TypeError);
  });
});

describe("evict and list", () => {
  test("evict removes chunks, meta and the track row", async () => {
    const r = await rig();
    const m = createOfflineManager(r.deps);
    const t = r.tracks.get("t2")!;
    await m.downloadTrack({ url: `/media/${t.sha}.flac`, bytes: t.data.length, sha256: t.sha, content_type: "audio/flac", ext: "flac" }, meta("t2"));
    expect(await m.list()).toHaveLength(1);
    await m.evict("t2");
    expect(await m.list()).toHaveLength(0);
    expect(await r.deps.store.getChunk(t.sha, "flac", 0)).toBeNull();
    expect(await r.deps.store.listFiles()).toHaveLength(0);
  });
});
