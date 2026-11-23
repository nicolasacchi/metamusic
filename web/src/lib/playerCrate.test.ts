import { describe, expect, test } from "bun:test";
import { Player, type DeviceHooks, type MediaLinks } from "./player.svelte";
import { morningItem, type MorningChapter, type MorningCrate, type MorningOutEvent } from "./morningCrate";

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
  duration = 400;
  paused = true;
  readyState = 4;
  ended = false;
  error: null = null;
  play() {
    this.paused = false;
    return Promise.resolve();
  }
  pause() {
    this.paused = true;
  }
  load() {}
  emit(type: string) {
    this.dispatchEvent(new Event(type));
  }
}

const T0 = Date.parse("2026-11-23T08:00:00Z");
const ch = (n: number, side: number, start: number, end: number): MorningChapter => ({
  anchor: null, audio_start_ms: start + 4000, end_ms: end, gain_db: null, limited: null, lufs_after: null, markers: 0, n, side,
  skipped_at_ms: null, stars: null, start_ms: start, strong_markers: 0, track: null, verdict: "unheard",
});
function view(): MorningCrate {
  const side = (i: number) => ({ bytes: 10, chapters: 2, duration_ms: 400_000, expires_at: "2026-11-23T20:00:00Z", label: `Side ${"AB"[i]}`, removed: false, sha256: "a".repeat(64), side: i, url: `/media/side${i}.flac?x=1` });
  return {
    adjacency_relaxed: false, anchors: 0, built_at: null, chapters: [ch(1, 0, 0, 200_000), ch(2, 0, 200_000, 400_000), ch(3, 1, 0, 200_000), ch(4, 1, 200_000, 400_000)],
    created_at: "2026-11-23T07:00:00Z", duration_ms: 800_000, error_code: null, id: "c1", playlist_id: null, revealed: false, revealed_at: null,
    sides: [side(0), side(1)], state: "ready", takes: 4, trigger: "scheduled",
  };
}

function setup() {
  const el = new FakeAudio();
  const sent: MorningOutEvent[] = [];
  const device: DeviceHooks = { quality: () => "lossless", connection: () => undefined, recordFailedProbe: () => {}, onGesture: () => {} };
  const p = new Player({
    fetchLinks: async (id): Promise<MediaLinks | null> => {
      const m = /^morning:c1:(\d)$/.exec(id);
      if (!m) return { track_id: id, expires_at: "2026-11-23T20:00:00Z", ttl_s: 1, cover: null, lossy: false, flac: { url: `/media/${id}.flac`, bytes: 1, content_type: "audio/flac", sample_rate: 44100, bits_per_sample: 16, sha256: "x" }, mp3: { url: `/media/${id}.mp3`, bytes: 1, content_type: "audio/mpeg", bitrate_kbps: 1, sha256: "y" } };
      return { track_id: id, expires_at: "2026-11-23T20:00:00Z", ttl_s: 1, cover: null, lossy: false, flac: { url: `/media/side${m[1]}.flac`, bytes: 1, content_type: "audio/flac", sample_rate: 0, bits_per_sample: 24, sha256: "z" }, mp3: { url: `/media/side${m[1]}.flac`, bytes: 1, content_type: "audio/flac", bitrate_kbps: 0, sha256: "z" } };
    },
    device,
    now: () => T0,
    submitMorning: (e) => void sent.push(e),
  });
  p.attach(el as unknown as HTMLAudioElement);
  const flush = () => new Promise((r) => setTimeout(r, 0));
  return { p, el, sent, flush };
}

describe("player crate mode", () => {
  test("playCrate plays the side file in crate mode with a blind label and the crate's artist", async () => {
    const { p, el, flush } = setup();
    p.playCrate(view(), 0, 0);
    await flush();
    expect(p.state.mode).toBe("crate");
    expect(p.state.queue).toEqual([morningItem("c1", 0), morningItem("c1", 1)]);
    expect(el.src).toBe("/media/side0.flac");
    expect(p.artist).toBe("Morning crate");
    expect(p.hasNext).toBe(true);
    expect(p.title).toBe("Take 1 of 4");
    expect(p.album).toBe("Side A");
    expect(p.state.format).toBe("flac");
  });

  test("a side that was removed or has no link is not playable", async () => {
    const { p, flush } = setup();
    const v = view();
    v.sides[1] = { ...v.sides[1]!, removed: true, url: null };
    p.playCrate(v, 1, 0);
    await flush();
    expect(p.state.mode).toBe("track");
    expect(p.state.trackId).toBe("");
    p.playCrate(v, 0, 0);
    expect(p.state.queue).toEqual([morningItem("c1", 0)]);
  });

  test("headset nexttrack skips and moves to the next chapter's slate; the lock screen follows", async () => {
    const { p, el, sent, flush } = setup();
    p.playCrate(view(), 0, 0);
    await flush();
    el.currentTime = 60;
    el.emit("timeupdate");
    p.crate!.press("nexttrack");
    expect(sent.map((e) => [e.kind, e.chapter, e.side_ms])).toEqual([["play", 1, 60_000], ["skip", 1, 60_000]]);
    expect(el.currentTime).toBe(200);
    el.emit("timeupdate");
    expect(p.title).toBe("Skipped 1"); // the flash owns the lock screen for 2 s, the new chapter's label waits behind it
    await new Promise((r) => setTimeout(r, 2100));
    expect(p.title).toBe("Take 2 of 4");
  });

  test("skipping the last chapter of a side starts the next side at its first slate", async () => {
    const { p, el, sent, flush } = setup();
    p.playCrate(view(), 0, 250_000);
    await flush();
    el.currentTime = 250;
    p.crate!.press("nexttrack");
    await flush();
    expect(p.state.trackId).toBe(morningItem("c1", 1));
    expect(sent.at(-1)?.kind).toBe("skip");
    expect(sent.at(-1)?.chapter).toBe(2);
    expect(el.src).toBe("/media/side1.flac");
  });

  test("pausing commits a buffered lone marker", async () => {
    const { p, el, sent, flush } = setup();
    p.playCrate(view(), 0, 0);
    await flush();
    el.currentTime = 30;
    el.emit("timeupdate");
    p.crate!.press("previoustrack");
    expect(sent.filter((e) => e.kind === "marker")).toEqual([]);
    el.emit("pause");
    expect(sent.filter((e) => e.kind === "marker").length).toBe(1);
  });

  test("on-screen next and previous never mark; previous goes to the chapter start", async () => {
    const { p, el, sent, flush } = setup();
    p.playCrate(view(), 0, 0);
    await flush();
    el.currentTime = 230;
    p.prev();
    expect(el.currentTime).toBe(200);
    expect(sent.filter((e) => e.kind === "marker" || e.kind === "strong_marker")).toEqual([]);
    p.next();
    expect(sent.at(-1)?.kind).toBe("skip");
  });

  test("a side that ends completes its last chapter once, and the next side plays", async () => {
    const { p, el, sent, flush } = setup();
    p.playCrate(view(), 0, 250_000);
    await flush();
    el.currentTime = 399;
    el.emit("timeupdate");
    el.ended = true;
    el.emit("ended");
    await flush();
    expect(sent.filter((e) => e.kind === "complete").map((e) => [e.chapter, e.side_ms])).toEqual([[2, 400_000]]);
    expect(p.state.trackId).toBe(morningItem("c1", 1));
    expect(el.src).toBe("/media/side1.flac");
    expect(p.state.mode).toBe("crate");
  });

  test("playing a take leaves crate mode: markers committed, session gone, the app's artist back", async () => {
    const { p, el, sent, flush } = setup();
    p.playCrate(view(), 0, 0);
    await flush();
    el.currentTime = 30;
    p.crate!.press("previoustrack");
    p.play("t1");
    await flush();
    expect(p.state.mode).toBe("track");
    expect(p.crate).toBeNull();
    expect(p.artist).toBe("metamusic");
    expect(sent.map((e) => e.kind)).toEqual(["marker"]);
  });

  test("a seek by the listener forgets the chapter left: no complete for a jump", async () => {
    const { p, el, sent, flush } = setup();
    p.playCrate(view(), 0, 0);
    await flush();
    el.currentTime = 10;
    el.emit("timeupdate");
    p.seek(250);
    el.emit("timeupdate");
    expect(sent.map((e) => `${e.kind}${e.chapter}`)).toEqual(["play1", "play2"]);
  });

  test("the flash shows for 2 s and the chapter label returns", async () => {
    const { p, el, flush } = setup();
    p.playCrate(view(), 0, 0);
    await flush();
    el.currentTime = 72;
    el.emit("timeupdate");
    p.crate!.press("previoustrack");
    expect(p.title).toBe("Marker 1 @ 1:12");
    await new Promise((r) => setTimeout(r, 2100));
    expect(p.title).toBe("Take 1 of 4");
    p.crate!.dispose();
  });
});
