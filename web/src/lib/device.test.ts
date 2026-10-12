import { describe, expect, test } from "bun:test";
import { Device, type DeviceApi, type ProbeAudio, type ProbeInfo, type ProfileBody } from "./device.svelte";

class FakeProbeAudio extends EventTarget implements ProbeAudio {
  src = "";
  private _t = 0;
  /** A real element fires `seeked` then `canplay` after the seek is requested. */
  get currentTime() {
    return this._t;
  }
  set currentTime(v: number) {
    this._t = v;
    setTimeout(() => {
      this.emit("seeked");
      this.emit("canplay");
    }, 0);
  }
  duration = 3;
  error: { code: number } | null = null;
  loads = 0;
  flac = "maybe";
  canPlayType() {
    return this.flac;
  }
  load() {
    this.loads++;
  }
  emit(type: string) {
    this.dispatchEvent(new Event(type));
  }
  fail(code: number) {
    this.error = { code };
    this.emit("error");
  }
  /** Healthy device: metadata, then seek, then canplay. */
  succeed() {
    this.emit("loadedmetadata");
  }
}

const INFO: ProbeInfo = { version: 1, url: "/media/probe.flac?exp=1", duration_s: 3, seek_to_s: 1.5, expires_at: "2099-01-01T00:00:00Z" };

class MemStore {
  m = new Map<string, string>();
  getItem(k: string) {
    return this.m.get(k) ?? null;
  }
  setItem(k: string, v: string) {
    this.m.set(k, v);
  }
}

function setup(over: { timeoutMs?: number; store?: MemStore } = {}) {
  const calls: string[] = [];
  const puts: { id: string; body: ProfileBody }[] = [];
  const audios: FakeProbeAudio[] = [];
  const api: DeviceApi = {
    async probe() {
      calls.push("probe");
      return INFO;
    },
    async put(id, body) {
      puts.push({ id, body });
      return true;
    },
    async list() {
      return [];
    },
  };
  const store = over.store ?? new MemStore();
  const dev = new Device({
    api,
    storage: store,
    createAudio: () => {
      const a = new FakeProbeAudio();
      audios.push(a);
      return a;
    },
    userAgent: "TestUA/1.0",
    connection: () => undefined,
    now: () => Date.parse("2026-01-01T00:00:00Z"),
    timeoutMs: over.timeoutMs ?? 20,
    isHidden: () => false,
    onHidden: () => () => {},
  });
  return { dev, calls, puts, audios, store };
}
const tick = () => new Promise((r) => setTimeout(r, 0));

describe("device probe", () => {
  test("boot calls only get_device_probe and creates no media element", async () => {
    const { dev, calls, puts, audios } = setup();
    await dev.boot();
    expect(calls).toEqual(["probe"]);
    expect(puts).toEqual([]);
    expect(audios).toEqual([]);
  });

  test("device id is generated once and persisted", () => {
    const { dev, store } = setup();
    expect(dev.deviceId).toMatch(/^[A-Za-z0-9_-]{8,64}$/);
    expect(store.getItem("mm:device-id")).toBe(dev.deviceId);
    expect(setup({ store }).dev.deviceId).toBe(dev.deviceId);
  });

  test("the probe starts synchronously inside the gesture callback", async () => {
    const { dev, audios } = setup();
    await dev.boot();
    dev.onGesture();
    expect(audios.length).toBe(1);
    expect(audios[0]!.src).toBe(INFO.url);
    expect(audios[0]!.loads).toBe(1);
  });

  test("no gesture before boot finished: no probe", () => {
    const { dev, audios } = setup();
    dev.onGesture();
    expect(audios).toEqual([]);
  });

  test("success stores ok with decode/seek true and leaves quality alone", async () => {
    const { dev, puts, audios } = setup();
    await dev.boot();
    dev.onGesture();
    audios[0]!.succeed();
    await dev.probing;
    expect(puts.length).toBe(1);
    expect(puts[0]!.id).toBe(dev.deviceId);
    expect(puts[0]!.body).toMatchObject({
      probe_result: "ok",
      flac_decode_ok: true,
      flac_seek_ok: true,
      quality: "lossless",
      quality_source: "default",
      probe_version: 1,
      flac_can_play: "maybe",
      user_agent: "TestUA/1.0",
    });
    expect(dev.quality()).toBe("lossless");
    expect(dev.probeResult).toBe("ok");
    // A stored version-1 result skips further probes.
    dev.onGesture();
    expect(audios.length).toBe(1);
  });

  test("SRC_NOT_SUPPORTED and DECODE fail the probe and downgrade", async () => {
    for (const code of [4, 3]) {
      const { dev, puts, audios } = setup();
      await dev.boot();
      dev.onGesture();
      audios[0]!.fail(code);
      await dev.probing;
      expect(puts[0]!.body).toMatchObject({ probe_result: "failed", quality: "high", quality_source: "probe", flac_decode_ok: false });
      expect(dev.quality()).toBe("high");
    }
  });

  test("timeout is inconclusive: NULL decode/seek, quality unchanged", async () => {
    const { dev, puts, audios } = setup({ timeoutMs: 5 });
    await dev.boot();
    dev.onGesture();
    await dev.probing;
    expect(audios.length).toBe(1);
    expect(puts[0]!.body.probe_result).toBe("inconclusive");
    expect(puts[0]!.body.flac_decode_ok).toBeUndefined();
    expect(puts[0]!.body.flac_seek_ok).toBeUndefined();
    expect(puts[0]!.body.quality).toBe("lossless");
    expect(dev.quality()).toBe("lossless");
  });

  test("MEDIA_ERR_NETWORK and ABORTED are inconclusive", async () => {
    for (const code of [2, 1]) {
      const { dev, puts, audios } = setup();
      await dev.boot();
      dev.onGesture();
      audios[0]!.fail(code);
      await dev.probing;
      expect(puts[0]!.body.probe_result).toBe("inconclusive");
      expect(dev.quality()).toBe("lossless");
    }
  });

  test("duration mismatch is inconclusive", async () => {
    const { dev, puts, audios } = setup();
    await dev.boot();
    dev.onGesture();
    audios[0]!.duration = 2.5;
    audios[0]!.succeed();
    await dev.probing;
    expect(puts[0]!.body.probe_result).toBe("inconclusive");
  });

  test("a user-chosen quality is never overridden by a failed probe", async () => {
    const { dev, puts, audios } = setup();
    await dev.boot();
    await dev.setQuality("auto");
    expect(puts.at(-1)!.body).toMatchObject({ quality: "auto", quality_source: "user" });
    dev.onGesture();
    // quality_source user: the gesture probe (a) does not run
    expect(audios.length).toBe(0);
    // the Settings button probes anyway
    dev.testNow();
    audios[0]!.fail(4);
    await dev.probing;
    expect(puts.at(-1)!.body).toMatchObject({ probe_result: "failed", quality: "auto", quality_source: "user" });
    expect(dev.quality()).toBe("auto");
  });

  test("at most 3 inconclusive attempts per version", async () => {
    const { dev, audios } = setup({ timeoutMs: 2 });
    await dev.boot();
    for (let i = 0; i < 5; i++) {
      dev.onGesture();
      await dev.probing;
      await tick();
    }
    expect(audios.length).toBe(3);
  });

  test("recordFailedProbe (player fallback) downgrades unless user-chosen", async () => {
    const a = setup();
    a.dev.recordFailedProbe();
    await tick();
    expect(a.dev.quality()).toBe("high");
    expect(a.puts.at(-1)!.body).toMatchObject({ probe_result: "failed", quality: "high", quality_source: "probe" });
    const b = setup();
    await b.dev.setQuality("lossless");
    b.dev.recordFailedProbe();
    await tick();
    expect(b.dev.quality()).toBe("lossless");
  });

  test("auto follows saveData/cellular through connection()", () => {
    const { dev } = setup();
    expect(dev.connection()).toBeUndefined();
  });
});
