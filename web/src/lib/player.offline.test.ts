import { describe, expect, test } from "bun:test";
import { createOutbox, memoryStore } from "../api/outbox";
import type { LocalSource } from "../api/offline";
import { createRecorder } from "./listen.svelte";
import { Player, type DeviceHooks, type LocalHooks, type MediaLinks } from "./player.svelte";

class FakeAudio extends EventTarget {
  private _src = "";
  get src() {
    return this._src;
  }
  set src(v: string) {
    this._src = v;
    this.paused = true;
    queueMicrotask(() => this.dispatchEvent(new Event("emptied")));
  }
  currentTime = 0;
  duration = NaN;
  paused = true;
  ended = false;
  readyState = 0;
  error: { code: number; message: string } | null = null;
  play() {
    this.paused = false;
    queueMicrotask(() => this.dispatchEvent(new Event("play")));
    return Promise.resolve();
  }
  pause() {
    this.paused = true;
  }
  load() {
    this.paused = true;
  }
  fail(code: number) {
    this.error = { code, message: "" };
    this.dispatchEvent(new Event("error"));
  }
}

const T0 = Date.parse("2026-01-01T00:00:00Z");
const sha = (c: string) => c.repeat(64);

function links(id: string): MediaLinks {
  return {
    track_id: id,
    expires_at: new Date(T0 + 12 * 3600_000).toISOString(),
    ttl_s: 12 * 3600,
    cover: null,
    lossy: false,
    flac: { url: `/media/${id}.flac?exp=1`, bytes: 1, content_type: "audio/flac", sample_rate: 44100, bits_per_sample: 16, sha256: "x" },
    mp3: { url: `/media/${id}.mp3?exp=1`, bytes: 1, content_type: "audio/mpeg", bitrate_kbps: 192, sha256: "y" },
  };
}

interface Opts {
  controller?: boolean;
  online?: boolean;
  locals?: Record<string, LocalSource>;
}

function setup(o: Opts = {}) {
  const el = new FakeAudio();
  const calls = { links: [] as string[], meta: [] as string[], probe: 0, put: 0, suspect: [] as string[] };
  const device: DeviceHooks = { quality: () => "lossless", connection: () => undefined, recordFailedProbe: () => void calls.probe++ };
  const locals = o.locals ?? {};
  const local: LocalHooks = {
    source: async (id) => locals[id] ?? null,
    meta: async (id) => (locals[id] ? { title: "Local " + id, album: "Local album" } : null),
    markSuspect: (id) => void calls.suspect.push(id),
  };
  const p = new Player({
    fetchLinks: async (id) => (calls.links.push(id), links(id)),
    device,
    now: () => T0,
    fetchMeta: async (id) => (calls.meta.push(id), { title: "Remote " + id, album: "" }),
    local,
    hasController: () => o.controller ?? true,
    online: () => o.online ?? true,
  });
  p.attach(el as unknown as HTMLAudioElement);
  const flush = () => new Promise((r) => setTimeout(r, 0));
  return { p, el, calls, flush };
}

const loc = (c: string, fmt: "flac" | "mp3" = "flac"): LocalSource => ({ src: `/media/${sha(c)}.${fmt}`, contentType: fmt === "flac" ? "audio/flac" : "audio/mpeg", format: fmt, coverSrc: null });

describe("local source selection (C115.4)", () => {
  test("a complete local file with a controlling worker plays /media/<sha>.<ext> and mints no link", async () => {
    const { p, el, calls, flush } = setup({ locals: { a: loc("a") } });
    p.play("a", ["a"]);
    await flush();
    expect(el.src).toBe(`/media/${sha("a")}.flac`);
    expect(calls.links).toEqual([]);
    expect(p.state.format).toBe("flac");
  });

  test("an mp3 local file reports its format", async () => {
    const { p, el, flush } = setup({ locals: { a: loc("a", "mp3") } });
    p.play("a", ["a"]);
    await flush();
    expect(el.src).toBe(`/media/${sha("a")}.mp3`);
    expect(p.state.format).toBe("mp3");
  });

  test("the same file without a controller takes the signed-link path", async () => {
    const { p, el, calls, flush } = setup({ controller: false, locals: { a: loc("a") } });
    p.play("a", ["a"]);
    await flush();
    expect(calls.links).toEqual(["a"]);
    expect(el.src).toBe("/media/a.flac?exp=1");
  });

  test("a track that is not downloaded takes the signed-link path", async () => {
    const { p, el, calls, flush } = setup({ locals: { a: loc("a") } });
    p.play("b", ["b"]);
    await flush();
    expect(calls.links).toEqual(["b"]);
    expect(el.src).toBe("/media/b.flac?exp=1");
  });

  test("the next local track starts synchronously at ended", async () => {
    const { p, el, calls, flush } = setup({ locals: { a: loc("a"), b: loc("b") } });
    p.play("a", ["a", "b"]);
    await flush();
    el.dispatchEvent(new Event("playing"));
    await flush();
    el.ended = true;
    el.dispatchEvent(new Event("ended"));
    expect(el.src).toBe(`/media/${sha("b")}.flac`); // no await: applied synchronously
    expect(calls.links).toEqual([]);
  });
});

describe("a media error on a local source", () => {
  for (const code of [2, 3, 4]) {
    test(`code ${code}: exactly one re-mint at the same position, no MP3 switch, no probe`, async () => {
      const { p, el, calls, flush } = setup({ locals: { a: loc("a") } });
      p.play("a", ["a"]);
      await flush();
      el.dispatchEvent(new Event("playing"));
      el.currentTime = 42;
      el.dispatchEvent(new Event("timeupdate"));
      el.fail(code);
      await flush();
      expect(calls.links).toEqual(["a"]);
      expect(el.src).toBe("/media/a.flac?exp=1"); // the signed FLAC link, not the MP3
      expect(p.state.format).toBe("flac");
      expect(calls.suspect).toEqual(["a"]);
      expect(calls.probe).toBe(0);
      el.currentTime = 0;
      el.dispatchEvent(new Event("loadedmetadata"));
      expect(el.currentTime).toBe(42);
    });
  }

  test("the suspect file is not retried on the same page life", async () => {
    const { p, el, calls, flush } = setup({ locals: { a: loc("a") } });
    p.play("a", ["a"]);
    await flush();
    el.fail(4);
    await flush();
    p.play("a", ["a"]);
    await flush();
    expect(el.src).toBe("/media/a.flac?exp=1");
    expect(calls.links.length).toBeGreaterThanOrEqual(1);
  });
});

describe("offline playback needs no API call", () => {
  test("no get_track / get_media_links; metadata from the local store; listen events land in the outbox", async () => {
    const { p, el, calls, flush } = setup({ online: false, locals: { a: loc("a"), b: loc("b") } });
    const box = createOutbox({
      store: memoryStore(),
      post: async () => ({ kind: "retry" }),
      beacon: () => true,
      keepalive: () => {},
      now: () => T0,
    });
    let n = 0;
    const rec = createRecorder({
      player: p as unknown as Parameters<typeof createRecorder>[0]["player"],
      emit: (e) => void box.append(e),
      sessionId: "s",
      deviceId: () => "d",
      id: () => `00000000-0000-7000-8000-${String(++n).padStart(12, "0")}`,
      now: () => new Date(T0),
    });
    p.play("a", ["a", "b"]);
    await flush();
    expect(p.title).toBe("Local a");
    expect(p.album).toBe("Local album");
    el.duration = 100;
    el.dispatchEvent(new Event("play"));
    el.currentTime = 100;
    el.ended = true;
    el.dispatchEvent(new Event("ended"));
    await flush();
    await box.idle();
    rec.destroy();
    expect(calls.links).toEqual([]);
    expect(calls.meta).toEqual([]);
    const kinds = (await box.snapshot()).map((e) => e.kind);
    expect(kinds).toContain("start");
    expect(kinds).toContain("complete");
    expect(await box.pending()).toBe(kinds.length);
  });

  test("online, metadata comes from the API (titles may have changed)", async () => {
    const { p, calls, flush } = setup({ online: true, locals: { a: loc("a") } });
    p.play("a", ["a"]);
    await flush();
    expect(calls.meta).toEqual(["a"]);
    expect(p.title).toBe("Remote a");
  });
});
