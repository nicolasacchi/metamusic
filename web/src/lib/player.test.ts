import { describe, expect, test } from "bun:test";
import { Player, formatFor, type DeviceHooks, type MediaLinks } from "./player.svelte";

class FakeAudio extends EventTarget {
  src = "";
  currentTime = 0;
  duration = NaN;
  paused = true;
  error: { code: number } | null = null;
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
  emit(type: string) {
    this.dispatchEvent(new Event(type));
  }
  fail(code: number) {
    this.error = { code };
    this.emit("error");
  }
}

function links(id: string): MediaLinks {
  return {
    track_id: id,
    expires_at: new Date(Date.now() + 12 * 3600_000).toISOString(),
    ttl_s: 12 * 3600,
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
  let now = Date.now();
  const p = new Player({
    fetchLinks: async (id) => {
      fetched.push(id);
      return links(id);
    },
    device,
    now: () => now,
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
    expect(el.log).toEqual(["play"]);
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
    expect(failed).toHaveLength(1);
    expect(el.src).toBe("/media/a.mp3?exp=1");
    expect(p.state.format).toBe("mp3");
    el.duration = 30;
    el.currentTime = 0;
    el.emit("loadedmetadata");
    expect(el.currentTime).toBe(7);
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
    el.emit("loadedmetadata");
    p.load("a", undefined, { startAt: 30 });
    expect(el.currentTime).toBe(30);
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
});
