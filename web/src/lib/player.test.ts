import { describe, expect, test } from "bun:test";
import { Player, formatFor, resolveMeta, type DeviceHooks, type MediaLinks, type TrackRef } from "./player.svelte";

class FakeAudio extends EventTarget {
  private _src = "";
  /** Models the media load algorithm: a src change pauses without `pause`, then fires emptied. */
  get src() {
    return this._src;
  }
  set src(v: string) {
    this._src = v;
    this.paused = true;
    queueMicrotask(() => this.emit("emptied"));
  }
  currentTime = 0;
  duration = NaN;
  paused = true;
  readyState = 0;
  error: { code: number; message: string } | null = null;
  log: string[] = [];
  setSeekLog: number[] = [];
  play() {
    this.log.push("play");
    this.paused = false;
    return Promise.resolve();
  }
  pause() {
    this.paused = true;
  }
  load() {
    this.log.push("load");
    this.paused = true;
    queueMicrotask(() => this.emit("emptied"));
  }
  emit(type: string) {
    this.dispatchEvent(new Event(type));
  }
  fail(code: number, message = "") {
    this.error = { code, message };
    this.emit("error");
  }
  /** Chromium order: the element pauses itself and fires `pause` before `error`. */
  failChromium(code: number) {
    this.paused = true;
    this.emit("pause");
    this.fail(code);
  }
}

const T0 = Date.parse("2026-01-01T00:00:00Z");

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

function setup(quality = "lossless") {
  const el = new FakeAudio();
  const fetched: string[] = [];
  const failed: number[] = [];
  const gestures: number[] = [];
  const device: DeviceHooks = {
    quality: () => quality,
    connection: () => undefined,
    recordFailedProbe: () => void failed.push(1),
    onGesture: () => void gestures.push(1),
  };
  let now = T0;
  const p = new Player({
    fetchLinks: async (id) => {
      fetched.push(id);
      return links(id);
    },
    device,
    now: () => now,
    fetchMeta: async (id) => ({ title: "Song " + id.toUpperCase(), album: "" }),
  });
  p.attach(el as unknown as HTMLAudioElement);
  const flush = () => new Promise((r) => setTimeout(r, 0));
  return { p, el, fetched, failed, gestures, flush, advance: (ms: number) => (now += ms) };
}

describe("quality to format", () => {
  test("mapping", () => {
    expect(formatFor("lossless")).toBe("flac");
    expect(formatFor("high")).toBe("mp3");
    expect(formatFor("auto")).toBe("flac");
    expect(formatFor("auto", { saveData: true })).toBe("mp3");
    expect(formatFor("auto", { type: "cellular" })).toBe("mp3");
    expect(formatFor("lossless", { saveData: true })).toBe("flac");
  });
});

describe("player store", () => {
  test("play sets src from links and calls play(); gesture hook fires", async () => {
    const { p, el, flush, gestures } = setup();
    p.play("a", ["a", "b"]);
    expect(gestures).toHaveLength(1);
    await flush();
    expect(el.src).toBe("/media/a.flac?exp=1");
    expect(el.log).toEqual(["load", "play"]);
    expect(p.state.format).toBe("flac");
  });

  test("high quality plays the mp3", async () => {
    const { p, el, flush } = setup("high");
    p.play("a");
    await flush();
    expect(el.src).toBe("/media/a.mp3?exp=1");
  });

  test("ended starts the prefetched next track synchronously", async () => {
    const { p, el, fetched, flush } = setup();
    p.play("a", ["a", "b"]);
    await flush();
    el.emit("playing");
    await flush();
    expect(fetched).toEqual(["a", "b"]);
    el.log.length = 0;
    const order: string[] = [];
    Object.defineProperty(el, "src", {
      set(v: string) {
        order.push("src:" + v);
      },
      get: () => "",
    });
    el.play = () => {
      order.push("play");
      return Promise.resolve();
    };
    el.emit("ended"); // no await: everything must have happened by now
    expect(order).toEqual(["src:/media/b.flac?exp=1", "play"]);
    expect(p.state.trackId).toBe("b");
    expect(p.state.index).toBe(1);
  });

  test("ended without prefetch falls back to fetching links", async () => {
    const { p, el, flush, fetched } = setup();
    p.play("a", ["a", "b"]);
    await flush();
    fetched.length = 0;
    el.emit("ended");
    await flush();
    expect(el.src).toBe("/media/b.flac?exp=1");
  });

  test("MEDIA_ERR_NETWORK re-mints once and restores the position", async () => {
    const { p, el, fetched, flush } = setup();
    p.play("a");
    await flush();
    el.currentTime = 42;
    el.fail(2);
    await flush();
    expect(fetched).toEqual(["a", "a"]);
    el.duration = 100;
    el.currentTime = 0;
    el.emit("loadedmetadata");
    expect(el.currentTime).toBe(42);
    el.fail(2);
    await flush();
    expect(fetched).toEqual(["a", "a"]);
    expect(p.state.error).not.toBe("");
  });

  test("SRC_NOT_SUPPORTED on FLAC switches to MP3 at the same position and records a failed probe", async () => {
    const { p, el, failed, flush } = setup();
    p.play("a");
    await flush();
    el.currentTime = 7;
    el.fail(4);
    await flush();
    expect(failed).toHaveLength(0);
    expect(el.src).toBe("/media/a.mp3?exp=1");
    expect(p.state.format).toBe("mp3");
    el.duration = 30;
    el.currentTime = 0;
    el.emit("loadedmetadata");
    expect(el.currentTime).toBe(7);
    expect(failed).toHaveLength(1);
    // an error on the mp3 does not record again
    el.fail(4);
    expect(failed).toHaveLength(1);
  });

  test("deep-link seek is applied in loadedmetadata, not before", async () => {
    const { p, el, flush } = setup();
    p.load("a", undefined, { startAt: 72 });
    await flush();
    expect(el.src).toBe("/media/a.flac?exp=1");
    expect(el.log).toEqual([]); // paused
    expect(el.currentTime).toBe(0);
    el.duration = 200;
    el.emit("loadedmetadata");
    expect(el.currentTime).toBe(72);
  });

  test("load on the already-current track seeks immediately", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    await flush();
    el.duration = 200;
    el.readyState = 1;
    el.emit("loadedmetadata");
    p.load("a", undefined, { startAt: 30 });
    expect(el.currentTime).toBe(30);
  });

  test("load on the current track with no metadata yet defers the seek", async () => {
    const { p, el, flush } = setup();
    p.load("a");
    await flush();
    p.load("a", undefined, { startAt: 30 });
    expect(el.currentTime).toBe(0);
    el.duration = 200;
    el.emit("loadedmetadata");
    expect(el.currentTime).toBe(30);
  });

  test("a start position survives the old source's metadata while the new links are fetched", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    await flush();
    el.duration = 200;
    el.readyState = 1;
    el.emit("loadedmetadata");
    // jump to another track: the unlock load() re-reads the old src, whose metadata may arrive before the links
    p.play("b", undefined, { startAt: 8 });
    el.emit("loadedmetadata");
    expect(el.src).toBe("/media/a.flac?exp=1");
    expect(el.currentTime).toBe(0); // not applied to the old source
    await flush();
    expect(el.src).toBe("/media/b.flac?exp=1");
    el.emit("loadedmetadata");
    expect(el.currentTime).toBe(8);
  });

  test("expired cached links are fetched again", async () => {
    const { p, fetched, flush, advance } = setup();
    p.play("a");
    await flush();
    advance(12 * 3600_000);
    p.play("a");
    await flush();
    expect(fetched).toEqual(["a", "a"]);
  });

  test("next, prev and seek clamp", async () => {
    const { p, el, flush } = setup();
    p.play("a", ["a", "b", "c"]);
    await flush();
    p.next();
    await flush();
    expect(p.state.trackId).toBe("b");
    el.duration = 10;
    p.seek(99);
    expect(el.currentTime).toBe(10);
    p.state.current = 0;
    p.prev();
    await flush();
    expect(p.state.trackId).toBe("a");
  });

  test("toggle pauses and resumes", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    await flush();
    p.toggle();
    expect(el.paused).toBe(true);
    p.toggle();
    expect(el.paused).toBe(false);
  });

  test("ended, next and prev switch the title to the new track", async () => {
    const { p, el, flush } = setup();
    p.play("a", ["a", "b", "c"], { title: "Song A" });
    await flush();
    expect(p.title).toBe("Song A");
    el.emit("playing");
    await flush();
    el.emit("ended");
    await flush();
    expect(p.state.trackId).toBe("b");
    expect(p.title).toBe("Song B");
    p.next();
    await flush();
    expect(p.title).toBe("Song C");
    p.state.current = 0;
    p.prev();
    await flush();
    expect(p.title).toBe("Song B");
  });

  test("stale title is cleared while the next one resolves; a late result for an old track is dropped", async () => {
    const { p, el, flush } = setup();
    const waiters = new Map<string, (m: { title: string; album: string }) => void>();
    p.configure({ fetchMeta: (id) => new Promise((r) => waiters.set(id, r)) });
    p.play("a", ["a", "b"], { title: "Song A" });
    await flush();
    el.emit("ended");
    expect(p.title).not.toBe("Song A");
    p.play("a", ["a", "b"], { title: "Song A" });
    waiters.get("b")?.({ title: "Late B", album: "" });
    await flush();
    expect(p.title).toBe("Song A");
  });

  test("title changes announce themselves on the element", async () => {
    const { p, el, flush } = setup();
    let n = 0;
    el.addEventListener("mm:meta", () => n++);
    p.play("a", ["a", "b"], { title: "Song A" });
    await flush();
    const before = n;
    el.emit("ended");
    await flush();
    expect(n).toBeGreaterThan(before);
  });

  test("FLAC code 4 on an expired link re-mints and records no failed probe", async () => {
    const { p, el, fetched, failed, flush, advance } = setup();
    p.play("a");
    await flush();
    advance(13 * 3600_000);
    el.currentTime = 5;
    el.fail(4);
    await flush();
    expect(fetched).toEqual(["a", "a"]);
    expect(failed).toHaveLength(0);
    expect(p.state.format).toBe("flac");
    expect(el.src).toBe("/media/a.flac?exp=1");
  });

  test("FLAC code 4 on a valid link falls back to MP3 and records a failed probe", async () => {
    const { p, el, failed, flush } = setup();
    p.play("a");
    await flush();
    el.fail(4);
    await flush();
    el.emit("loadedmetadata");
    expect(failed).toHaveLength(1);
    expect(p.state.format).toBe("mp3");
  });

  test("FLAC code 4 then MP3 code 4 records nothing and shows an error", async () => {
    const { p, el, failed, flush } = setup();
    p.play("a");
    await flush();
    el.fail(4);
    await flush();
    el.fail(4);
    await flush();
    el.emit("loadedmetadata");
    expect(failed).toHaveLength(0);
    expect(p.state.error).not.toBe("");
  });

  test("Firefox audio sink error (code 3) is not a codec failure: no MP3 fallback, no failed probe", async () => {
    const { p, el, failed, flush } = setup();
    p.play("a");
    await flush();
    el.emit("playing");
    el.fail(3, "OnMediaSinkAudioError");
    await flush();
    expect(failed).toHaveLength(0);
    expect(p.state.format).toBe("flac");
    expect(p.state.playing).toBe(true);
    expect(p.state.error).toBe("");
  });

  describe("an ended that follows a Firefox audio-sink error", () => {
    /** Plays "a" (queue a, b) for `at` seconds of a 100 s track, then raises the sink error. */
    async function sunk(at = 20) {
      const s = setup();
      s.p.play("a", ["a", "b"]);
      await s.flush();
      s.el.duration = 100;
      s.el.currentTime = at;
      s.el.emit("playing");
      s.el.fail(3, "OnMediaSinkAudioError");
      return s;
    }
    const end = (el: FakeAudio) => {
      (el as unknown as { ended: boolean }).ended = true;
      const e = new Event("ended", { cancelable: true });
      let seen = false;
      el.addEventListener("ended", () => (seen = true)); // a later listener, like the listen recorder
      el.dispatchEvent(e);
      return seen;
    };

    test("before the clock could reach the end it reloads the same source at the error position and does not advance", async () => {
      const { p, el, flush, advance } = await sunk(20);
      advance(5_000); // 25 s of clock: far from the end of a 100 s track
      el.currentTime = 100;
      el.log.length = 0;
      expect(end(el)).toBe(false); // the recorder never sees a finish
      await flush();
      expect(p.state.trackId).toBe("a");
      expect(el.src).toBe("/media/a.flac?exp=1");
      expect(el.log).toContain("play");
      el.duration = 100;
      el.emit("loadedmetadata");
      expect(el.currentTime).toBe(20);
    });

    test("a clock that really reached the end is a normal finish (headless Firefox without a device)", async () => {
      const { p, el, advance } = await sunk(20);
      advance(80_000);
      el.currentTime = 100;
      expect(end(el)).toBe(true);
      expect(p.state.trackId).toBe("b");
    });

    test("it gives up after two reloads so a dead device still moves the queue on", async () => {
      const { p, el, flush, advance } = await sunk(20);
      for (let i = 0; i < 2; i++) {
        advance(1_000);
        end(el);
        await flush();
        expect(p.state.trackId).toBe("a");
        el.duration = 100;
        el.emit("loadedmetadata");
        el.fail(3, "OnMediaSinkAudioError");
      }
      advance(1_000);
      end(el);
      expect(p.state.trackId).toBe("b");
    });

    test("a deliberate seek forgets the error: an end near the new position is a finish", async () => {
      const { p, el, advance } = await sunk(20);
      p.seek(98);
      advance(1_000);
      end(el);
      expect(p.state.trackId).toBe("b");
    });

    test("another track's error does not make this track's end premature", async () => {
      const { p, el, advance } = await sunk(20);
      p.play("b", ["a", "b", "c"]);
      el.duration = 100;
      advance(1_000);
      end(el);
      expect(p.state.trackId).toBe("c");
    });
  });

  describe("MEDIA_ERR_ABORTED (Android Firefox aborts the media fetch when backgrounded)", () => {
    async function playing(at = 20) {
      const s = setup();
      s.p.play("a", ["a", "b"]);
      await s.flush();
      s.el.duration = 100;
      s.el.currentTime = at;
      s.el.emit("playing");
      return s;
    }

    test("code 1 reloads the same source at the current position instead of leaving a half-dead element", async () => {
      const { p, el, flush } = await playing(20);
      el.log.length = 0;
      el.fail(1);
      await flush();
      expect(p.state.trackId).toBe("a");
      expect(p.state.format).toBe("flac");
      expect(el.src).toBe("/media/a.flac?exp=1");
      expect(el.log).toContain("play");
      el.currentTime = 0; // a reloaded element starts at 0 ...
      el.emit("loadedmetadata");
      expect(el.currentTime).toBe(20); // ... and is put back
    });

    test("an expired link is minted again", async () => {
      const { el, fetched, flush, advance } = await playing(20);
      advance(13 * 3600_000);
      const before = fetched.length;
      el.fail(1);
      await flush();
      expect(fetched.length).toBe(before + 1);
    });

    test("an abort we caused (a track change before the new source is applied) is ignored", async () => {
      const { p, el, fetched, flush } = await playing(20);
      p.play("b", ["a", "b"]);
      const n = fetched.length;
      el.fail(1); // the old source's late abort
      await flush();
      expect(p.state.trackId).toBe("b");
      expect(el.src).toBe("/media/b.flac?exp=1");
      expect(fetched.filter((x) => x === "a")).toHaveLength(fetched.slice(0, n).filter((x) => x === "a").length);
    });

    test("after three reloads the error stays; an early ended is then refused and reloaded, not a finish", async () => {
      const { p, el, flush, advance } = await playing(20);
      for (let i = 0; i < 3; i++) {
        el.fail(1);
        await flush();
        el.duration = 100;
        el.currentTime = 20;
        el.emit("loadedmetadata");
      }
      el.fail(1);
      await flush();
      expect(p.state.error).toBe("Playback error");
      advance(2_000);
      el.currentTime = 22; // the buffer ran out
      (el as unknown as { ended: boolean }).ended = true;
      let seen = false;
      el.addEventListener("ended", () => (seen = true));
      el.dispatchEvent(new Event("ended", { cancelable: true }));
      await flush();
      expect(seen).toBe(false);
      expect(p.state.trackId).toBe("a");
      el.emit("loadedmetadata");
      expect(el.currentTime).toBe(22); // resumes where the stream stopped
    });

    test("a code 2 that cannot be re-minted twice also guards the end", async () => {
      const { p, el, flush, advance } = await playing(20);
      el.fail(2);
      await flush();
      el.duration = 100;
      el.currentTime = 20;
      el.emit("loadedmetadata");
      el.fail(2); // second network error before `playing`: gives up
      await flush();
      advance(1_000);
      (el as unknown as { ended: boolean }).ended = true;
      el.dispatchEvent(new Event("ended", { cancelable: true }));
      expect(p.state.trackId).toBe("a");
    });
  });

  test("the MP3 fallback after a failed re-mint resumes where the song was, not at 0", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    await flush();
    el.duration = 100;
    el.currentTime = 30;
    el.emit("timeupdate");
    el.fail(2);
    await flush();
    el.currentTime = 0; // the reset element
    el.emit("timeupdate");
    el.fail(4); // the re-minted FLAC fails to load: MP3 fallback
    await flush();
    expect(p.state.format).toBe("mp3");
    el.emit("loadedmetadata");
    expect(el.currentTime).toBe(30);
  });

  test("Chromium event order: re-mint after code 2 still resumes playback", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    await flush();
    el.log.length = 0;
    el.failChromium(2);
    await flush();
    expect(el.log).toContain("play");
  });

  test("Chromium event order: MP3 fallback after code 4 still resumes playback", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    await flush();
    el.log.length = 0;
    el.failChromium(4);
    await flush();
    expect(el.src).toBe("/media/a.mp3?exp=1");
    expect(el.log).toContain("play");
  });

  test("a later network failure after a successful re-mint is recovered again", async () => {
    const { p, el, fetched, flush } = setup();
    p.play("a");
    await flush();
    el.fail(2);
    await flush();
    el.emit("playing");
    el.fail(2);
    await flush();
    expect(fetched).toEqual(["a", "a", "a"]);
  });

  test("toggle re-mint keeps a queued deep-link seek", async () => {
    const { p, el, flush, advance } = setup();
    p.load("a");
    await flush();
    advance(13 * 3600_000);
    p.load("a", undefined, { startAt: 30 });
    p.toggle();
    await flush();
    el.duration = 100;
    el.emit("loadedmetadata");
    expect(el.currentTime).toBe(30);
  });

  test("meta failure falls back to a neutral title and retries on playing", async () => {
    const el = new FakeAudio();
    let ok = false;
    let calls = 0;
    const p = new Player({
      fetchLinks: async (id) => links(id),
      device: { quality: () => "lossless", connection: () => undefined, recordFailedProbe() {} },
      now: () => T0,
      fetchMeta: async () => {
        calls++;
        return ok ? { title: "Real", album: "" } : null;
      },
    });
    p.attach(el as unknown as HTMLAudioElement);
    p.play("a");
    await new Promise((r) => setTimeout(r, 0));
    expect(p.title).toBe("Untitled track");
    ok = true;
    el.emit("playing");
    await new Promise((r) => setTimeout(r, 0));
    expect(p.title).toBe("Real");
    expect(calls).toBeGreaterThan(1);
  });

  test("loadMeta de-duplicates concurrent requests", async () => {
    const el = new FakeAudio();
    let calls = 0;
    const p = new Player({
      fetchLinks: async (id) => links(id),
      device: { quality: () => "lossless", connection: () => undefined, recordFailedProbe() {} },
      now: () => T0,
      fetchMeta: async (id) => {
        calls++;
        await new Promise((r) => setTimeout(r, 5));
        return { title: id, album: "" };
      },
    });
    p.attach(el as unknown as HTMLAudioElement);
    p.play("a", ["a", "b"]);
    await new Promise((r) => setTimeout(r, 0));
    el.emit("playing"); // prefetches b
    p.next(); // syncMeta(b) while prefetch is in flight
    await new Promise((r) => setTimeout(r, 20));
    expect(calls).toBe(2);
  });

  test("resolveMeta uses own title as album for a root and the root title for a child", async () => {
    const db: Record<string, TrackRef> = {
      r: { title: "Root", rootId: "r" },
      c: { title: "Child", rootId: "r" },
    };
    const get = async (id: string) => db[id] ?? null;
    expect(await resolveMeta("r", get)).toEqual({ title: "Root", album: "Root" });
    expect(await resolveMeta("c", get)).toEqual({ title: "Child", album: "Root" });
    expect(await resolveMeta("zz", get)).toBeNull();
  });

  test("FLAC code 4 after the one re-mint falls back to MP3", async () => {
    const { p, el, failed, flush, advance } = setup();
    p.play("a");
    await flush();
    advance(13 * 3600_000);
    el.fail(4);
    await flush();
    el.fail(4);
    await flush();
    el.emit("loadedmetadata");
    expect(failed).toHaveLength(1);
    expect(p.state.format).toBe("mp3");
  });

  test("MEDIA_ERR_DECODE on FLAC falls back to MP3", async () => {
    const { p, el, failed, flush } = setup();
    p.play("a");
    await flush();
    el.fail(3);
    await flush();
    el.emit("loadedmetadata");
    expect(failed).toHaveLength(1);
    expect(el.src).toBe("/media/a.mp3?exp=1");
  });

  test("recovery keeps a paused element paused", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    await flush();
    p.toggle();
    expect(el.paused).toBe(true);
    el.log.length = 0;
    el.fail(2);
    await flush();
    expect(el.log).not.toContain("play");
  });

  test("an uncached play unlocks the element synchronously inside the gesture", async () => {
    const { p, el } = setup();
    p.play("a");
    expect(el.log).toEqual(["load"]);
  });

  test("NotAllowedError surfaces a tap-to-play state", async () => {
    const { p, el, flush } = setup();
    el.play = () => Promise.reject(new DOMException("blocked", "NotAllowedError"));
    p.play("a");
    await flush();
    expect(p.state.error).toMatch(/tap/i);
    expect(p.state.playing).toBe(false);
  });

  test("toggle on a never-fetched element with expired links re-applies fresh links", async () => {
    const { p, el, fetched, flush, advance } = setup();
    p.load("a");
    await flush();
    advance(13 * 3600_000);
    fetched.length = 0;
    p.toggle();
    await flush();
    expect(fetched).toEqual(["a"]);
    expect(el.log).toContain("play");
  });

  test("link cache boundary is expires_at minus 10 minutes", async () => {
    const { p, fetched, flush, advance } = setup();
    p.play("a");
    await flush();
    advance(12 * 3600_000 - 10 * 60_000 - 1);
    p.play("a");
    await flush();
    expect(fetched).toEqual(["a"]);
    advance(1);
    p.play("a");
    await flush();
    expect(fetched).toEqual(["a", "a"]);
  });
});

describe("player store review fixes", () => {
  test("loading another track while playing leaves playing false", async () => {
    const { p, el, flush } = setup();
    p.play("a", ["a", "b"]);
    await flush();
    el.emit("playing");
    expect(p.state.playing).toBe(true);
    p.load("b", ["a", "b"], { startAt: 5 });
    await flush();
    expect(p.state.playing).toBe(false);
  });

  test("links failure after play(uncached) over a playing track leaves playing false", async () => {
    const { p, el, flush } = setup();
    p.play("a", ["a", "b"]);
    await flush();
    el.emit("playing");
    p.configure({ fetchLinks: async () => null });
    p.play("c", ["a", "c"]);
    await flush();
    expect(p.state.playing).toBe(false);
    expect(p.state.error).toBe("Could not load media links");
  });

  test("title-only play still resolves the album", async () => {
    const { p, flush } = setup();
    p.configure({ fetchMeta: async () => ({ title: "Other", album: "Root" }) });
    p.play("a", ["a"], { title: "Song A" });
    expect(p.title).toBe("Song A");
    await flush();
    expect(p.title).toBe("Song A");
    expect(p.album).toBe("Root");
  });

  test("user pause during an in-flight link fetch is honoured", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    p.pause();
    await flush();
    expect(el.paused).toBe(true);
    expect(el.log).not.toContain("play");
  });

  test("load of the same track while its links are in flight keeps play intent and queues the seek", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    p.load("a", undefined, { startAt: 9 });
    await flush();
    expect(el.log).toContain("play");
    el.readyState = 1;
    el.duration = 100;
    el.emit("loadedmetadata");
    expect(el.currentTime).toBe(9);
  });

  test("toggle after a failed FLAC and MP3 re-applies the source", async () => {
    const { p, el, flush } = setup();
    p.play("a");
    await flush();
    el.fail(4);
    await flush();
    el.fail(4);
    expect(p.state.error).toBe("Playback error");
    el.log.length = 0;
    el.paused = true;
    p.toggle();
    await flush();
    expect(el.src).toContain("/media/a.");
    expect(el.log).toContain("play");
  });
});

describe("lossy tracks (M2c)", () => {
  const lossyLinks = (id: string): MediaLinks => ({
    track_id: id,
    expires_at: new Date(T0 + 12 * 3600_000).toISOString(),
    ttl_s: 12 * 3600,
    cover: null,
    lossy: true,
    flac: null,
    mp3: { url: `/media/${id}.mp3?exp=1`, bytes: 1, content_type: "audio/mpeg", bitrate_kbps: 320, sha256: "y" },
  });

  test("lossy track plays mp3 and never probes", async () => {
    const { p, el, flush, failed } = setup("lossless");
    p.configure({ fetchLinks: async (id) => lossyLinks(id) });
    p.play("a");
    await flush();
    expect(el.src).toBe("/media/a.mp3?exp=1");
    expect(p.state.format).toBe("mp3");
    expect(p.state.noFlac).toBe(true);
    // A decode failure of the mp3 must not enter the FLAC fallback machinery.
    el.fail(4);
    el.emit("loadedmetadata");
    await flush();
    expect(failed).toHaveLength(0);
    expect(p.state.error).toBe("Playback error");
    expect((p as unknown as { forceMp3: Set<string> }).forceMp3.size).toBe(0);
    expect((p as unknown as { probePending: Set<string> }).probePending.size).toBe(0);
  });

  test("noFlac follows the track: a lossless track clears it", async () => {
    const { p, flush } = setup("lossless");
    p.configure({ fetchLinks: async (id) => (id === "a" ? lossyLinks(id) : links(id)) });
    p.play("a", ["a", "b"]);
    await flush();
    expect(p.state.noFlac).toBe(true);
    p.play("b", ["a", "b"]);
    await flush();
    expect(p.state.noFlac).toBe(false);
    expect(p.state.format).toBe("flac");
  });
});
