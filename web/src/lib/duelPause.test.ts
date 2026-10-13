import { describe, expect, test } from "bun:test";
import { Player, type MediaLinks } from "./player.svelte";

class El extends EventTarget {
  src = "";
  currentTime = 0;
  duration = NaN;
  paused = true;
  readyState = 4;
  error = null;
  plays = 0;
  play() {
    this.plays++;
    this.paused = false;
    this.dispatchEvent(new Event("play"));
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
    lossy: false,
    flac: { url: `/media/${id}.flac?exp=1`, bytes: 1, content_type: "audio/flac", sample_rate: 44100, bits_per_sample: 16, sha256: "x" },
    mp3: { url: `/media/${id}.mp3?exp=1`, bytes: 1, content_type: "audio/mpeg", bitrate_kbps: 192, sha256: "y" },
  };
}

describe("duel start pauses THE audio element", () => {
  test("pause() pauses the element and clears playing; toggle() resumes", async () => {
    const el = new El();
    const p = new Player({
      fetchLinks: async (id) => links(id),
      device: { quality: () => "lossless", connection: () => undefined, recordFailedProbe() {} },
      now: () => Date.now(),
    });
    p.attach(el as unknown as HTMLAudioElement);
    p.play("t1");
    await new Promise((r) => setTimeout(r, 0));
    expect(p.state.playing).toBe(true);
    p.pause();
    expect(el.paused).toBe(true);
    expect(p.state.playing).toBe(false);
    const before = el.plays;
    p.toggle();
    expect(el.plays).toBe(before + 1);
    expect(p.state.playing).toBe(true);
  });
});
