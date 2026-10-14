// Auto-advance hardening (Android Firefox: the next track did not start by itself).
import { describe, expect, test } from "bun:test";
import { Player, type DeviceHooks, type MediaLinks } from "./player.svelte";
import { createRecorder, type ListenEvent } from "./listen.svelte";

class FakeAudio extends EventTarget {
  private _src = "";
  get src() {
    return this._src;
  }
  set src(v: string) {
    this._src = v;
    this.paused = true;
    this.currentTime = 0;
    this.duration = NaN;
    queueMicrotask(() => this.emit("emptied"));
  }
  currentTime = 0;
  duration = NaN;
  paused = true;
  readyState = 0;
  error: { code: number; message: string } | null = null;
  playImpl: () => Promise<void> = () => Promise.resolve();
  plays = 0;
  play() {
    this.plays++;
    return this.playImpl().then(() => {
      this.paused = false;
    });
  }
  pause() {
    this.paused = true;
  }
  load() {
    this.paused = true;
  }
  emit(type: string) {
    this.dispatchEvent(new Event(type));
  }
}

const T0 = Date.parse("2026-01-01T00:00:00Z");

function links(id: string, at = T0, ttl = 12 * 3600): MediaLinks {
  return {
    track_id: id,
    expires_at: new Date(at + ttl * 1000).toISOString(),
    ttl_s: ttl,
    lossy: false,
    flac: { url: `/media/${id}.flac`, bytes: 1, content_type: "audio/flac", sample_rate: 44100, bits_per_sample: 16, sha256: "x" },
    mp3: { url: `/media/${id}.mp3`, bytes: 1, content_type: "audio/mpeg", bitrate_kbps: 192, sha256: "y" },
  };
}

function setup() {
  const el = new FakeAudio();
  const fetched: string[] = [];
  const device: DeviceHooks = { quality: () => "lossless", connection: () => undefined, recordFailedProbe() {} };
  let now = T0;
  const p = new Player({
    fetchLinks: async (id) => {
      fetched.push(id);
      return links(id, now);
    },
    device,
    now: () => now,
    fetchMeta: async (id) => ({ title: id, album: "" }),
  });
  p.attach(el as unknown as HTMLAudioElement);
  const flush = () => new Promise((r) => setTimeout(r, 0));
  const diag: { kind: string; detail?: string; track_id?: string; pos_ms?: number }[] = [];
  el.addEventListener("mm:diag", (e) => diag.push((e as CustomEvent).detail));
  return { p, el, fetched, flush, diag, advance: (ms: number) => (now += ms) };
}

/** A playing track `a` of `dur` seconds with queue a,b. */
async function playing(queue = ["a", "b"], dur = 100) {
  const s = setup();
  s.p.play(queue[0]!, queue);
  await s.flush();
  s.el.duration = dur;
  s.el.readyState = 4;
  s.el.emit("loadedmetadata");
  s.el.paused = false;
  s.el.emit("playing");
  await s.flush();
  return s;
}

class FakeDoc extends EventTarget {
  visibilityState = "visible";
}

describe("complete event survives the synchronous advance (real-device bug)", () => {
  test("a track change between the player's and the recorder's ended listeners still yields complete", async () => {
    const { p, el } = await playing();
    const out: ListenEvent[] = [];
    let seq = 0;
    // Browsers run a microtask checkpoint after every listener; installListen's $effect runs sync() right
    // after the player's ended listener, i.e. before the recorder's own.
    const h: { rec?: ReturnType<typeof createRecorder> } = {};
    el.addEventListener("ended", () => h.rec?.sync());
    h.rec = createRecorder({
      player: p,
      emit: (e) => void out.push(e),
      sessionId: "session-1",
      deviceId: () => "dev",
      id: () => `id${++seq}`,
      now: () => new Date(T0),
    });
    el.emit("play");
    el.currentTime = 99.99;
    el.emit("timeupdate");
    el.currentTime = 100;
    el.emit("ended");
    expect(out.map((e) => e.kind)).toEqual(["start", "segment", "complete", "advance"]);
    expect(out[2]).toMatchObject({ track_id: "a", to_ms: 100000 });
  });
});

describe("queue semantics", () => {
  test("end of queue stops with a visible state and no error", async () => {
    const { p, el } = await playing(["a"]);
    el.emit("ended");
    expect(p.state.endOfQueue).toBe(true);
    expect(p.state.playing).toBe(false);
    expect(p.state.trackId).toBe("a");
    expect(p.hasNext).toBe(false);
  });

  test("playing again after the end clears the end-of-queue state and can end again", async () => {
    const { p, el } = await playing(["a"]);
    el.emit("ended");
    p.resume();
    el.emit("play");
    expect(p.state.endOfQueue).toBe(false);
    el.emit("ended");
    expect(p.state.endOfQueue).toBe(true);
  });

  test("play next inserts right after the current track and is played first", async () => {
    const { p, el, flush } = await playing(["a", "b", "c"]);
    p.playNext("z");
    expect(p.state.queue).toEqual(["a", "z", "b", "c"]);
    p.playNext("y");
    expect(p.state.queue).toEqual(["a", "z", "y", "b", "c"]);
    await flush(); // the new next track's link is prefetched
    el.emit("ended");
    expect(p.state.trackId).toBe("z");
    el.emit("ended");
    expect(p.state.trackId).toBe("y");
  });

  test("play next moves a track that is already queued after the current one", async () => {
    const { p } = await playing(["a", "b", "c"]);
    p.playNext("c");
    expect(p.state.queue).toEqual(["a", "c", "b"]);
  });

  test("enqueue appends and re-arms the end of queue", async () => {
    const { p, el, flush } = await playing(["a"]);
    el.emit("ended");
    expect(p.state.endOfQueue).toBe(true);
    p.enqueue("q");
    await flush();
    expect(p.state.queue).toEqual(["a", "q"]);
    expect(p.hasNext).toBe(true);
  });

  test("playing a track that is part of the queue without a queue keeps the queue", async () => {
    const { p } = await playing(["a", "b", "c"]);
    p.play("b");
    expect(p.state.queue).toEqual(["a", "b", "c"]);
    expect(p.state.index).toBe(1);
  });

  test("a queue source extends the queue before it runs out", async () => {
    const { p, el, flush } = await playing(["a", "b"]);
    let calls = 0;
    p.setQueueSource(async () => {
      calls++;
      return calls === 1 ? ["c", "d"] : [];
    });
    el.emit("playing"); // prefetchNext asks the source when the queue is about to run out
    await flush();
    expect(p.state.queue).toEqual(["a", "b", "c", "d"]);
    await flush();
    expect(calls).toBe(1);
  });
});

describe("prefetch (a): ended never awaits", () => {
  test("the next link is fetched at track start and again at 80 % when the cache expired", async () => {
    const { p, el, fetched, flush, advance } = await playing();
    expect(fetched).toEqual(["a", "b"]);
    advance(13 * 3600_000); // b's cached link expired
    el.currentTime = 80;
    el.emit("timeupdate");
    await flush();
    expect(fetched).toEqual(["a", "b", "b"]);
    el.currentTime = 85;
    el.emit("timeupdate");
    await flush();
    expect(fetched).toHaveLength(3); // cached again: no refetch storm
    el.emit("ended");
    expect(p.state.trackId).toBe("b");
    expect(el.src).toBe("/media/b.flac");
  });

  test("a link that failed at track start is retried at 80 %", async () => {
    const s = setup();
    let fail = true;
    s.p.configure({
      fetchLinks: async (id) => {
        s.fetched.push(id);
        if (id === "b" && fail) throw new Error("offline");
        return links(id, T0);
      },
    });
    s.p.play("a", ["a", "b"]);
    await s.flush();
    s.el.duration = 100;
    s.el.emit("playing");
    await s.flush();
    fail = false;
    s.el.currentTime = 81;
    s.el.emit("timeupdate");
    await s.flush();
    s.el.emit("ended");
    expect(s.el.src).toBe("/media/b.flac");
    expect(s.el.plays).toBe(2);
  });
});

describe("autoplay blocked (b)", () => {
  test("a rejected play() after ended keeps the queue position and a pending-play state", async () => {
    const { p, el, flush, diag } = await playing();
    el.playImpl = () => Promise.reject(new DOMException("blocked", "NotAllowedError"));
    el.emit("ended");
    await flush();
    expect(p.state.trackId).toBe("b");
    expect(p.state.index).toBe(1);
    expect(p.state.pendingPlay).toBe(true);
    expect(p.state.playing).toBe(false);
    expect(p.state.error).toMatch(/tap/i);
    expect(diag.map((d) => d.kind)).toContain("autoplay_blocked");
  });

  test("the next user interaction resumes the pending play and clears the state", async () => {
    const { p, el, flush } = await playing();
    const doc = new FakeDoc();
    p.watchActivation(doc as unknown as Document);
    el.playImpl = () => Promise.reject(new DOMException("blocked", "NotAllowedError"));
    el.emit("ended");
    await flush();
    el.playImpl = () => Promise.resolve();
    const before = el.plays;
    doc.dispatchEvent(new Event("click"));
    await flush();
    expect(el.plays).toBe(before + 1);
    expect(p.state.pendingPlay).toBe(false);
    expect(p.state.error).toBe("");
    expect(p.state.trackId).toBe("b");
  });

  test("becoming visible retries the pending play; an interaction without a pending play does nothing", async () => {
    const { p, el, flush } = await playing();
    const doc = new FakeDoc();
    p.watchActivation(doc as unknown as Document);
    const before = el.plays;
    doc.dispatchEvent(new Event("click"));
    await flush();
    expect(el.plays).toBe(before);
    el.playImpl = () => Promise.reject(new DOMException("blocked", "NotAllowedError"));
    el.emit("ended");
    await flush();
    el.playImpl = () => Promise.resolve();
    const n = el.plays;
    doc.dispatchEvent(new Event("visibilitychange"));
    await flush();
    expect(el.plays).toBe(n + 1);
  });

  test("Media Session play (resume) clears the pending play", async () => {
    const { p, el, flush } = await playing();
    el.playImpl = () => Promise.reject(new DOMException("blocked", "NotAllowedError"));
    el.emit("ended");
    await flush();
    el.playImpl = () => Promise.resolve();
    p.resume();
    await flush();
    expect(p.state.pendingPlay).toBe(false);
  });

  test("a rejected play() of an explicit play also leaves a pending play", async () => {
    const { p, el, flush } = setup();
    el.playImpl = () => Promise.reject(new DOMException("blocked", "NotAllowedError"));
    p.play("a", ["a"]);
    await flush();
    expect(p.state.pendingPlay).toBe(true);
  });
});

describe("end-of-track watchdog (c)", () => {
  test("ended never fires: stalled within 0.3 s of the end for 1.5 s advances exactly once", async () => {
    const { p, el, advance, diag } = await playing();
    el.currentTime = 99.9;
    el.emit("timeupdate");
    p.tick(); // first sight of the stall
    advance(1000);
    p.tick();
    expect(p.state.trackId).toBe("a");
    advance(600);
    p.tick();
    expect(p.state.trackId).toBe("b");
    expect(el.src).toBe("/media/b.flac");
    advance(5000);
    p.tick();
    expect(p.state.index).toBe(1);
    expect(diag.filter((d) => d.kind === "advance")).toEqual([{ kind: "advance", detail: "reason=watchdog", track_id: "a", pos_ms: 99900 }]);
  });

  test("paused at the end (not by the user) also advances", async () => {
    const { p, el, advance } = await playing();
    el.currentTime = 99.95;
    el.paused = true;
    p.tick();
    advance(1600);
    p.tick();
    expect(p.state.trackId).toBe("b");
  });

  test("a user pause near the end does not advance", async () => {
    const { p, el, advance } = await playing();
    el.currentTime = 99.95;
    p.pause();
    p.tick();
    advance(5000);
    p.tick();
    expect(p.state.trackId).toBe("a");
  });

  test("progressing playback or mid-track stalls never trigger it", async () => {
    const { p, el, advance } = await playing();
    el.currentTime = 50;
    p.tick();
    advance(10_000);
    p.tick();
    expect(p.state.trackId).toBe("a");
    for (let t = 99.5; t <= 99.9; t += 0.1) {
      el.currentTime = t;
      advance(500);
      p.tick();
    }
    expect(p.state.trackId).toBe("a");
  });

  test("a late native ended while the next source is still being fetched does not skip a track", async () => {
    const { p, el, advance } = await playing(["a", "b", "c"]);
    p.configure({ fetchLinks: () => new Promise(() => {}) });
    (p as unknown as { cache: Map<string, unknown> }).cache.clear();
    el.currentTime = 99.95;
    p.tick();
    advance(1600);
    p.tick();
    expect(p.state.trackId).toBe("b");
    el.emit("ended"); // belongs to a, whose element was already swapped
    expect(p.state.trackId).toBe("b");
  });

  test("the watchdog at the last track ends the queue once", async () => {
    const { p, el, advance } = await playing(["a"]);
    el.currentTime = 99.95;
    el.paused = true;
    p.tick();
    advance(1600);
    p.tick();
    expect(p.state.endOfQueue).toBe(true);
    el.emit("ended");
    expect(p.state.endOfQueue).toBe(true);
  });

  test("complete is emitted exactly once per play when the watchdog and ended both run", async () => {
    const { p, el, advance } = await playing(["a"]);
    const out: ListenEvent[] = [];
    let seq = 0;
    createRecorder({
      player: p,
      emit: (e) => void out.push(e),
      sessionId: "session-1",
      deviceId: () => undefined,
      id: () => `id${++seq}`,
      now: () => new Date(T0),
    });
    el.emit("play");
    el.currentTime = 99.95;
    el.emit("timeupdate");
    p.tick();
    advance(1600);
    p.tick();
    el.emit("ended");
    expect(out.filter((e) => e.kind === "complete")).toHaveLength(1);
  });
});

describe("diagnostics (f)", () => {
  test("a media error is reported with its code", async () => {
    const { el, diag } = await playing();
    el.error = { code: 4, message: "x" };
    el.emit("error");
    expect(diag.find((d) => d.kind === "error")).toMatchObject({ detail: expect.stringContaining("code=4") });
  });

  test("a stall that ends is reported with its length", async () => {
    const { el, diag, advance } = await playing();
    el.emit("waiting");
    advance(2500);
    el.emit("playing");
    expect(diag.find((d) => d.kind === "stall_end")).toMatchObject({ detail: "ms=2500" });
  });

  test("a blip shorter than 500 ms is not reported", async () => {
    const { el, diag, advance } = await playing();
    el.emit("waiting");
    advance(100);
    el.emit("playing");
    expect(diag.find((d) => d.kind === "stall_end")).toBeUndefined();
  });

  test("an automatic advance is reported", async () => {
    const { el, diag } = await playing();
    el.emit("ended");
    expect(diag.find((d) => d.kind === "advance")).toMatchObject({ detail: "reason=ended", track_id: "a" });
  });

  test("the recorder turns diagnostics into outbox events for the right track", async () => {
    const { p, el } = await playing();
    const out: ListenEvent[] = [];
    let seq = 0;
    createRecorder({
      player: p,
      emit: (e) => void out.push(e),
      sessionId: "session-1",
      deviceId: () => "dev",
      id: () => `id${++seq}`,
      now: () => new Date(T0),
    });
    el.emit("ended");
    const adv = out.find((e) => e.kind === ("advance" as ListenEvent["kind"]));
    expect(adv).toMatchObject({ track_id: "a", detail: "reason=ended" });
  });
});
