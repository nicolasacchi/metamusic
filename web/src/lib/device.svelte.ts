// Device id, quality and the FLAC probe (C22.3). The probe only ever runs inside a user gesture:
// the first Play tap and the Settings "Test this device" button. Boot fetches the probe URL and
// creates no media element.
import { api } from "../api/client";
import type { components } from "../api/schema";
import { randomId } from "./id";
import type { Connection, DeviceHooks, Player, Quality } from "./player.svelte";

export type ProfileBody = components["schemas"]["PutDeviceProfileInBody"];
export type DeviceProfile = components["schemas"]["DeviceProfile"];
export type ProbeResult = "none" | "ok" | "failed" | "inconclusive";
export type ProbeInfo = Pick<components["schemas"]["DeviceProbeOutBody"], "version" | "url" | "duration_s" | "seek_to_s" | "expires_at">;

export interface DeviceApi {
  probe(): Promise<ProbeInfo | null>;
  put(id: string, body: ProfileBody): Promise<boolean>;
  list(): Promise<DeviceProfile[]>;
}

/** The slice of HTMLAudioElement the probe uses. */
export interface ProbeAudio extends EventTarget {
  src: string;
  currentTime: number;
  readonly duration: number;
  readonly error: { code: number } | null;
  canPlayType(type: string): string;
  load(): void;
}

export interface KV {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
}

export interface DeviceDeps {
  api: DeviceApi;
  storage: KV;
  createAudio(): ProbeAudio;
  userAgent: string;
  connection(): Connection | undefined;
  now(): number;
  timeoutMs: number;
  isHidden(): boolean;
  /** Subscribes to the page becoming hidden; returns the unsubscribe function. */
  onHidden(cb: () => void): () => void;
}

const PROBE_VERSION = 1;
const MAX_ATTEMPTS = 3;
const K = {
  id: "mm:device-id",
  quality: "mm:device-quality",
  source: "mm:device-quality-source",
  result: "mm:probe-result",
  attempts: "mm:probe-attempts",
};
const MEDIA_ERR_SRC_NOT_SUPPORTED = 4;
const MEDIA_ERR_DECODE = 3;
const TOLERANCE_S = 0.1;

function isQuality(v: string | null): v is Quality {
  return v === "lossless" || v === "high" || v === "auto";
}

export class Device implements DeviceHooks {
  deviceId: string;
  /** Result of the last probe stored for PROBE_VERSION ("none" until one was established). */
  probeResult = $state<ProbeResult>("none");
  qualityValue = $state<Quality>("lossless");
  qualitySource = $state<"default" | "user" | "probe">("default");
  /** Resolves when the running probe (and its PUT) is finished. */
  probing: Promise<void> = Promise.resolve();
  busy = $state(false);

  private deps: DeviceDeps;
  private info: ProbeInfo | null = null;
  private canPlay = "";
  private decodeOk: boolean | undefined;
  private seekOk: boolean | undefined;

  constructor(deps: DeviceDeps) {
    this.deps = deps;
    let id = deps.storage.getItem(K.id);
    if (!id || !/^[A-Za-z0-9_-]{8,64}$/.test(id)) {
      id = randomId();
      deps.storage.setItem(K.id, id);
    }
    this.deviceId = id;
    const q = deps.storage.getItem(K.quality);
    if (isQuality(q)) this.qualityValue = q;
    const s = deps.storage.getItem(K.source);
    if (s === "user" || s === "probe") this.qualitySource = s;
    const r = deps.storage.getItem(K.result);
    if (r === "ok" || r === "failed") this.probeResult = r;
  }

  listProfiles(): Promise<DeviceProfile[]> {
    return this.deps.api.list();
  }

  quality(): Quality {
    return this.qualityValue;
  }

  connection(): Connection | undefined {
    return this.deps.connection();
  }

  /** Boot: only fetch the probe URL so it is ready for the first gesture. */
  async boot(): Promise<void> {
    try {
      this.info = await this.deps.api.probe();
    } catch {
      this.info = null;
    }
  }

  /** Called synchronously inside the Play gesture. Starts the probe when it is due; never throws. */
  onGesture(): void {
    if (this.busy) return;
    if (this.probeResult !== "none" || this.qualitySource === "user") return;
    if (this.attempts() >= MAX_ATTEMPTS) return;
    this.startProbe();
  }

  /** Settings "Test this device" (also a gesture). */
  testNow(): void {
    if (this.busy) return;
    this.startProbe();
  }

  async setQuality(q: Quality): Promise<void> {
    this.qualityValue = q;
    this.qualitySource = "user";
    this.deps.storage.setItem(K.quality, q);
    this.deps.storage.setItem(K.source, "user");
    await this.put(this.probeResult === "none" ? "none" : this.probeResult);
  }

  /** The player fell back to MP3 after FLAC failed to decode here: a failed probe. */
  recordFailedProbe(): void {
    this.decodeOk = false;
    this.seekOk = undefined;
    this.probing = this.finish("failed");
  }

  private attempts(): number {
    try {
      const m = JSON.parse(this.deps.storage.getItem(K.attempts) ?? "{}") as Record<string, number>;
      return m[String(PROBE_VERSION)] ?? 0;
    } catch {
      return 0;
    }
  }

  private bumpAttempts() {
    this.deps.storage.setItem(K.attempts, JSON.stringify({ [String(PROBE_VERSION)]: this.attempts() + 1 }));
  }

  /** Synchronous part: create the probe element and start loading. The async rest never delays playback. */
  private startProbe(): void {
    const info = this.info;
    if (!info || Date.parse(info.expires_at) <= this.deps.now()) {
      // URL missing or stale: fetch again for the next gesture.
      void this.boot();
      return;
    }
    const audio = this.deps.createAudio();
    audio.src = info.url;
    audio.load();
    this.busy = true;
    this.bumpAttempts();
    this.probing = this.run(audio, info);
  }

  private async run(audio: ProbeAudio, info: ProbeInfo): Promise<void> {
    let outcome: ProbeResult = "inconclusive";
    this.decodeOk = undefined;
    this.seekOk = undefined;
    this.canPlay = audio.canPlayType("audio/flac");
    let hidden = false;
    const offHidden = this.deps.onHidden(() => {
      hidden = true;
      cancel(null);
    });
    let cancel: (v: ProbeResult | null) => void = () => {};
    const wait = (types: string[]): Promise<ProbeResult | "event"> =>
      new Promise((resolve) => {
        const offs: (() => void)[] = [];
        const done = (v: ProbeResult | "event") => {
          offs.forEach((f) => f());
          clearTimeout(timer);
          cancel = () => {};
          resolve(v);
        };
        const timer = setTimeout(() => done("inconclusive"), this.deps.timeoutMs);
        cancel = () => done("inconclusive");
        for (const t of types) {
          const h = () => done("event");
          audio.addEventListener(t, h);
          offs.push(() => audio.removeEventListener(t, h));
        }
        const onErr = () => {
          const c = audio.error?.code;
          done(c === MEDIA_ERR_SRC_NOT_SUPPORTED || c === MEDIA_ERR_DECODE ? "failed" : "inconclusive");
        };
        audio.addEventListener("error", onErr);
        offs.push(() => audio.removeEventListener("error", onErr));
      });
    try {
      if (this.deps.isHidden()) hidden = true;
      const meta = await wait(["loadedmetadata"]);
      if (meta === "failed") outcome = "failed";
      else if (meta === "event" && !hidden && Math.abs(audio.duration - info.duration_s) <= TOLERANCE_S) {
        // Seeked then canplay, either order of arrival within the window.
        // canplay may fire in the same task as seeked: record it before the seek is requested.
        let sawCanplay = false;
        const onCanplay = () => (sawCanplay = true);
        audio.addEventListener("canplay", onCanplay);
        const seeked = wait(["seeked"]);
        audio.currentTime = info.seek_to_s;
        const s = await seeked;
        audio.removeEventListener("canplay", onCanplay);
        if (s === "failed") outcome = "failed";
        else if (s === "event") {
          const c = sawCanplay ? "event" : await wait(["canplay"]);
          if (c === "failed") outcome = "failed";
          else if (c === "event") outcome = "ok";
        }
      }
      if (hidden && outcome === "ok") outcome = "inconclusive";
    } finally {
      offHidden();
    }
    if (outcome === "ok") {
      this.decodeOk = true;
      this.seekOk = true;
    } else if (outcome === "failed") {
      this.decodeOk = false;
    }
    await this.finish(outcome);
  }

  private async finish(outcome: ProbeResult): Promise<void> {
    if (outcome === "failed" && this.qualitySource !== "user") {
      this.qualityValue = "high";
      this.qualitySource = "probe";
      this.deps.storage.setItem(K.quality, "high");
      this.deps.storage.setItem(K.source, "probe");
    }
    if (outcome === "ok" || outcome === "failed") {
      this.probeResult = outcome;
      this.deps.storage.setItem(K.result, outcome);
    }
    try {
      await this.put(outcome);
    } finally {
      this.busy = false;
    }
  }

  private async put(result: ProbeResult): Promise<void> {
    const body: ProfileBody = {
      label: this.label(),
      user_agent: this.deps.userAgent,
      quality: this.qualityValue,
      quality_source: this.qualitySource,
      probe_result: result,
      flac_can_play: this.canPlay,
      probe_version: PROBE_VERSION,
      // eslint-disable-next-line svelte/prefer-svelte-reactivity
      probed_at: new Date(this.deps.now()).toISOString(),
    };
    if (result === "ok" || result === "failed") {
      if (this.decodeOk !== undefined) body.flac_decode_ok = this.decodeOk;
      if (this.seekOk !== undefined) body.flac_seek_ok = this.seekOk;
    }
    if (result === "none") delete body.probed_at;
    try {
      await this.deps.api.put(this.deviceId, body);
    } catch {
      /* the profile is advisory; the local state already applies */
    }
  }

  private label(): string {
    const ua = this.deps.userAgent;
    const m = /(Firefox|Edg|Chrome|Safari)\/[\d.]+/.exec(ua);
    const os = /(Android|iPhone|iPad|Windows|Macintosh|Linux)/.exec(ua);
    return [m?.[1] ?? "Browser", os?.[1] ?? ""].filter(Boolean).join(" on ").slice(0, 80);
  }
}

export function realDeviceApi(): DeviceApi {
  return {
    async probe() {
      const r = await api.GET("/api/device-probe");
      return r.data ?? null;
    },
    async put(id, body) {
      const r = await api.PUT("/api/device-profiles/{device_id}", { params: { path: { device_id: id } }, body });
      return r.response.ok;
    },
    async list() {
      const out: DeviceProfile[] = [];
      let cursor: string | undefined;
      for (let i = 0; i < 20; i++) {
        const r = await api.GET("/api/device-profiles", { params: { query: { limit: 100, ...(cursor ? { cursor } : {}) } } });
        if (!r.data) break;
        out.push(...r.data.items);
        if (!r.data.next_cursor) break;
        cursor = r.data.next_cursor;
      }
      return out;
    },
  };
}

let instance: Device | null = null;

/** The app-wide device; created on first use in the browser. */
export function getDevice(): Device {
  instance ??= new Device({
    api: realDeviceApi(),
    storage: localStorage,
    createAudio: () => new Audio(),
    userAgent: navigator.userAgent,
    connection: () => (navigator as Navigator & { connection?: Connection }).connection,
    now: () => Date.now(),
    timeoutMs: 10_000,
    isHidden: () => document.visibilityState === "hidden",
    onHidden: (cb) => {
      const h = () => {
        if (document.visibilityState === "hidden") cb();
      };
      document.addEventListener("visibilitychange", h);
      return () => document.removeEventListener("visibilitychange", h);
    },
  });
  return instance;
}

/** Hands the device to the player and fetches the probe URL. */
export function installDevice(player: Player): Device {
  const d = getDevice();
  player.configure({ device: d });
  void d.boot();
  return d;
}
