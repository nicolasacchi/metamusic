import { describe, expect, test } from "bun:test";
import { createAudioFocus, type FocusHost } from "./audioFocus";
import { Player, type DeviceHooks, type MediaLinks } from "./player.svelte";
import { installMediaSession, type MSEnv } from "./mediaSession";

function host(o: { playing?: boolean; track?: string; gesture?: boolean } = {}) {
  const log: string[] = [];
  const h = { playing: o.playing ?? true, track: o.track ?? "t1", gesture: o.gesture ?? true };
  const f: FocusHost = {
    wasPlaying: () => h.playing,
    trackId: () => h.track,
    suspend: () => void log.push("suspend"),
    resume: () => void log.push("resume"),
    hold: (on) => void log.push(`hold ${on}`),
    inGesture: () => h.gesture,
    changed: () => void log.push("changed"),
  };
  return { f, log, h, focus: createAudioFocus(f) };
}

describe("lease: acquire / release", () => {
  test("acquire suspends the player once; the same owner gets the same lease", () => {
    const { focus, log } = host();
    const a = focus.acquire("duel");
    const b = focus.acquire("duel");
    expect(b).toBe(a);
    expect(log.filter((x) => x === "suspend")).toHaveLength(1);
    expect(a.active).toBe(true);
    expect(focus.current()).toBe(a);
  });

  test("release({resume}) inside the gesture resumes at once and clears the hold", () => {
    const { focus, log } = host({ gesture: true });
    const l = focus.acquire("duel");
    log.length = 0;
    l.release({ resume: true });
    expect(log).toEqual(["hold false", "resume"]);
    expect(l.active).toBe(false);
    expect(focus.current()).toBeNull();
  });

  test("release outside a gesture does not resume: it leaves the Resume hold", () => {
    const { focus, log } = host({ gesture: false });
    const l = focus.acquire("duel");
    log.length = 0;
    l.release({ resume: true });
    expect(log).toEqual(["hold true", "changed"]);
  });

  test("an explicit gesture flag overrides the host's guess", () => {
    const a = host({ gesture: false });
    a.focus.acquire("duel").release({ resume: true, gesture: true });
    expect(a.log).toContain("resume");
    const b = host({ gesture: true });
    b.focus.acquire("duel").release({ resume: true, gesture: false });
    expect(b.log).not.toContain("resume");
    expect(b.log).toContain("hold true");
  });

  test("release without resume gives nothing back", () => {
    const { focus, log } = host();
    const l = focus.acquire("duel");
    log.length = 0;
    l.release();
    expect(log).not.toContain("resume");
    expect(log).not.toContain("hold true");
  });

  test("nothing was playing: nothing to resume, no hold", () => {
    const { focus, log } = host({ playing: false });
    const l = focus.acquire("duel");
    log.length = 0;
    l.release({ resume: true });
    expect(log).not.toContain("resume");
    expect(log).not.toContain("hold true");
  });

  test("the main track changed meanwhile: not resumed", () => {
    const { focus, log, h } = host();
    const l = focus.acquire("duel");
    h.track = "t2";
    log.length = 0;
    l.release({ resume: true });
    expect(log).not.toContain("resume");
  });

  test("release twice is a no-op", () => {
    const { focus, log } = host();
    const l = focus.acquire("duel");
    l.release({ resume: true });
    log.length = 0;
    l.release({ resume: true });
    expect(log).toEqual([]);
  });

  test("onEnd runs once however the lease ends", () => {
    for (const how of ["release", "interrupt"] as const) {
      const { focus } = host();
      let ended = 0;
      const l = focus.acquire("duel", { onEnd: () => void ended++ });
      if (how === "release") l.release({ resume: true });
      else focus.interrupt();
      l.release();
      expect(ended).toBe(1);
    }
  });
});

describe("lease: interrupt (the user touched the transport)", () => {
  test("ends the lease and a later release resumes nothing", () => {
    const { focus, log } = host();
    const l = focus.acquire("duel");
    focus.interrupt();
    expect(l.active).toBe(false);
    log.length = 0;
    l.release({ resume: true });
    expect(log).toEqual([]);
  });
  test("interrupt without a lease does nothing", () => {
    const { focus, log } = host();
    focus.interrupt();
    expect(log).toEqual([]);
  });
});

describe("lease: owners and handlers", () => {
  test("handlers are exposed only while the lease is held", () => {
    const { focus } = host();
    const hs = { play() {}, pause() {}, playing: () => false };
    expect(focus.handlers()).toBeNull();
    const l = focus.acquire("duel", { handlers: hs });
    expect(focus.handlers()).toBe(hs);
    l.release();
    expect(focus.handlers()).toBeNull();
  });
  test("a second owner inherits what the first found, so the music still comes back", () => {
    const { focus, log, h } = host();
    const duel = focus.acquire("duel");
    h.playing = false; // the player is paused now, because of the first lease
    const ears = focus.acquire("ears");
    expect(duel.active).toBe(false);
    expect(ears.owner).toBe("ears");
    log.length = 0;
    ears.release({ resume: true });
    expect(log).toContain("resume");
  });
});

// --- the Player: acquireFocus pauses without a user pause, release gives back ---------------------------

class FakeAudio extends EventTarget {
  src = "";
  currentTime = 0;
  duration = NaN;
  paused = true;
  readyState = 0;
  error = null;
  play() {
    this.paused = false;
    queueMicrotask(() => this.dispatchEvent(new Event("playing")));
    return Promise.resolve();
  }
  pause() {
    if (this.paused) return;
    this.paused = true;
    this.dispatchEvent(new Event("pause"));
  }
  load() {}
}
function links(id: string): MediaLinks {
  return {
    track_id: id,
    expires_at: new Date(Date.now() + 12 * 3600_000).toISOString(),
    ttl_s: 12 * 3600,
    cover: null,
    lossy: false,
    flac: { url: `/media/${id}.flac`, bytes: 1, content_type: "audio/flac", sample_rate: 44100, bits_per_sample: 16, sha256: "x" },
    mp3: { url: `/media/${id}.mp3`, bytes: 1, content_type: "audio/mpeg", bitrate_kbps: 192, sha256: "y" },
  };
}
function rig(gesture = true) {
  const el = new FakeAudio();
  const device: DeviceHooks = { quality: () => "lossless", connection: () => undefined, recordFailedProbe() {} };
  const p = new Player({ fetchLinks: async (id) => links(id), device, now: () => Date.now(), fetchMeta: async (id) => ({ title: `Title ${id}`, album: "" }), inGesture: () => gesture });
  p.attach(el as unknown as HTMLAudioElement);
  const flush = () => new Promise((r) => setTimeout(r, 0));
  return { p, el, flush };
}

describe("Player.acquireFocus", () => {
  test("a duel pauses the player, the verdict (release in the gesture) resumes it", async () => {
    const { p, el, flush } = rig(true);
    p.play("a", ["a", "b"]);
    await flush();
    expect(el.paused).toBe(false);
    const lease = p.acquireFocus("duel");
    expect(el.paused).toBe(true);
    expect(p.state.playing).toBe(false);
    expect(p.playIntent).toBe(false); // not "playing" for the lock screen
    lease.release({ resume: true });
    await flush();
    expect(el.paused).toBe(false);
    expect(p.playIntent).toBe(true);
    expect(p.state.held).toBe(false);
    expect(p.state.trackId).toBe("a");
  });

  test("release outside a gesture leaves a Resume hold; one tap on it plays", async () => {
    const { p, el, flush } = rig(false);
    p.play("a", ["a", "b"]);
    await flush();
    const lease = p.acquireFocus("duel");
    lease.release({ resume: true });
    await flush();
    expect(el.paused).toBe(true);
    expect(p.state.held).toBe(true);
    p.resume();
    await flush();
    expect(el.paused).toBe(false);
    expect(p.state.held).toBe(false);
  });

  test("a user transport action ends the lease: the later release resumes nothing", async () => {
    const { p, el, flush } = rig(true);
    p.play("a", ["a", "b"]);
    await flush();
    const lease = p.acquireFocus("duel");
    p.pause(); // the user's own pause
    expect(lease.active).toBe(false);
    lease.release({ resume: true });
    await flush();
    expect(el.paused).toBe(true);
    expect(p.state.held).toBe(false);
  });

  test("nothing was playing: release leaves it paused", async () => {
    const { p, el, flush } = rig(true);
    p.play("a", ["a"], { autoplay: false });
    await flush();
    p.acquireFocus("duel").release({ resume: true });
    await flush();
    expect(el.paused).toBe(true);
    expect(p.state.held).toBe(false);
  });

  test("Media Session play/pause control the duel while the lease is held, then the player again", async () => {
    const { p, el, flush } = rig(true);
    const handlers: Record<string, ((d: { seekTime?: number }) => void) | null> = {};
    const ms = { metadata: null as unknown, playbackState: "none", setActionHandler: (a: string, h: ((d: { seekTime?: number }) => void) | null) => void (handlers[a] = h), setPositionState() {} };
    const env: MSEnv = { mediaSession: ms, doc: new EventTarget() as unknown as Document, win: new EventTarget() as unknown as Window };
    installMediaSession(p, el as unknown as HTMLAudioElement, env);
    p.play("a", ["a", "b"]);
    await flush();
    const calls: string[] = [];
    let duelPlaying = true;
    const lease = p.acquireFocus("duel", {
      handlers: { play: () => (duelPlaying = true, void calls.push("duel play")), pause: () => (duelPlaying = false, void calls.push("duel pause")), playing: () => duelPlaying },
    });
    el.dispatchEvent(new Event("mm:state"));
    expect(ms.playbackState).toBe("playing"); // the duel is what is playing
    handlers.pause!({});
    lease.changed();
    expect(calls).toEqual(["duel pause"]);
    expect(el.paused).toBe(true);
    expect(p.state.trackId).toBe("a");
    expect(ms.playbackState).toBe("paused");
    handlers.play!({});
    expect(calls).toEqual(["duel pause", "duel play"]);
    expect(el.paused).toBe(true); // the main player stays out of it
    lease.release({ resume: true });
    await flush();
    expect(el.paused).toBe(false);
    handlers.pause!({});
    expect(el.paused).toBe(true); // lock screen controls the player again
    expect(calls).toHaveLength(2);
  });
});
