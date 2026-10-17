import { describe, expect, test } from "bun:test";
import { Player, type DeviceHooks, type MediaLinks } from "./player.svelte";
import {
  MAX_QUEUE,
  SAVE_EVERY_MS,
  STORAGE_KEY,
  TTL_MS,
  VERSION,
  capQueue,
  cleanSource,
  installPersistence,
  load,
  parse,
  save,
  serialize,
  type KV,
  type PersistEnv,
  type PlayerSnapshot,
} from "./playerStore";

const NOW = Date.parse("2026-10-17T12:00:00Z");

function kv(): KV & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
    removeItem: (k) => void data.delete(k),
  };
}

const snap = (o: Partial<PlayerSnapshot> = {}): PlayerSnapshot => ({
  queue: ["a", "b", "c", "d"],
  index: 1,
  queuedNext: 1,
  position: 72.46,
  trackId: "b",
  source: { label: "New since Tue", href: "/new" },
  ...o,
});

describe("round trip", () => {
  test("save then load returns the same player, stamped v1 and savedAt", () => {
    const s = kv();
    expect(save(s, snap(), NOW)).toBe(true);
    expect(load(s, NOW + 1000)).toEqual({
      v: 1,
      queue: ["a", "b", "c", "d"],
      index: 1,
      queuedNext: 1,
      position: 72.5,
      trackId: "b",
      source: { label: "New since Tue", href: "/new" },
      savedAt: NOW,
    });
    expect(STORAGE_KEY).toBe("mm:player:v1");
    expect(VERSION).toBe(1);
  });
  test("exactly the documented keys are stored", () => {
    const o = JSON.parse(serialize(snap(), NOW)!) as Record<string, unknown>;
    expect(Object.keys(o).sort()).toEqual(["index", "position", "queue", "queuedNext", "savedAt", "source", "trackId", "v"]);
  });
});

describe("TTL and version", () => {
  const rows: { name: string; at: number; ok: boolean }[] = [
    { name: "fresh", at: NOW, ok: true },
    { name: "just under 24 h", at: NOW - TTL_MS + 1, ok: true },
    { name: "24 h old", at: NOW - TTL_MS, ok: false },
    { name: "older", at: NOW - 3 * TTL_MS, ok: false },
    { name: "saved in the future (clock change)", at: NOW + 3600_000, ok: false },
  ];
  for (const r of rows) {
    test(r.name, () => {
      const s = kv();
      save(s, snap(), r.at);
      expect(load(s, NOW) !== null).toBe(r.ok);
      // a stale or foreign save is dropped, not kept around
      expect(s.data.has(STORAGE_KEY)).toBe(r.ok);
    });
  }
  test("another version is ignored", () => {
    const o = JSON.parse(serialize(snap(), NOW)!) as Record<string, unknown>;
    expect(parse(JSON.stringify({ ...o, v: 2 }), NOW)).toBeNull();
    expect(parse(JSON.stringify({ ...o, v: undefined }), NOW)).toBeNull();
  });
});

describe("malformed input", () => {
  const good = () => JSON.parse(serialize(snap(), NOW)!) as Record<string, unknown>;
  const rows: [string, (o: Record<string, unknown>) => unknown][] = [
    ["not json", () => "{nope"],
    ["not an object", () => "[]"],
    ["queue not an array", (o) => ({ ...o, queue: "abc" })],
    ["queue holds a non-id", (o) => ({ ...o, queue: ["a", 7, "c"] })],
    ["queue holds a URL", (o) => ({ ...o, queue: ["a", "https://x.example/b.flac?sig=1", "c"], trackId: "a" })],
    ["empty queue", (o) => ({ ...o, queue: [] })],
    ["track not in the queue", (o) => ({ ...o, trackId: "zzz" })],
    ["savedAt not a number", (o) => ({ ...o, savedAt: "now" })],
  ];
  for (const [name, f] of rows) {
    test(name, () => {
      const v = f(good());
      expect(parse(typeof v === "string" ? v : JSON.stringify(v), NOW)).toBeNull();
    });
  }
  test("index is rebuilt from the track, queuedNext and position are clamped", () => {
    const o = { ...good(), index: 99, queuedNext: 99, position: -5 };
    const r = parse(JSON.stringify(o), NOW)!;
    expect(r.index).toBe(1);
    expect(r.queuedNext).toBe(2);
    expect(r.position).toBe(0);
  });
});

describe("caps", () => {
  const ids = (n: number) => Array.from({ length: n }, (_, i) => `t${i}`);
  test("a queue over 500 ids is cut to 500 around the current track", () => {
    const queue = ids(1200);
    const out = JSON.parse(serialize(snap({ queue, trackId: "t700", index: 700, queuedNext: 2 }), NOW)!) as { queue: string[]; index: number; trackId: string };
    expect(out.queue).toHaveLength(MAX_QUEUE);
    expect(out.queue[out.index]).toBe("t700");
    expect(out.trackId).toBe("t700");
  });
  test("the current track near the end keeps 500 ids and stays in", () => {
    const r = capQueue(ids(900), 899, 0);
    expect(r.queue).toHaveLength(500);
    expect(r.queue[r.index]).toBe("t899");
  });
  test("a queue at the cap is untouched", () => {
    const r = capQueue(ids(500), 3, 1);
    expect(r.queue).toHaveLength(500);
    expect(r.index).toBe(3);
  });
  test("parse refuses a stored queue over the cap", () => {
    const o = { ...JSON.parse(serialize(snap(), NOW)!), queue: ids(501), trackId: "t3" };
    expect(parse(JSON.stringify(o), NOW)).toBeNull();
  });
});

describe("no URLs, no tokens", () => {
  test("ids that look like URLs or paths are dropped on save", () => {
    const raw = serialize(snap({ queue: ["a", "/media/x.flac?exp=1&sig=abc", "b", "http://h/x", "c"], trackId: "b", index: 2 }), NOW)!;
    const o = JSON.parse(raw) as { queue: string[]; index: number };
    expect(o.queue).toEqual(["a", "b", "c"]);
    expect(o.queue[o.index]).toBe("b");
    expect(raw).not.toMatch(/https?:|sig=|exp=|\.flac|\.mp3|token/i);
  });
  test("a current track that is not an id is not saved at all", () => {
    expect(serialize(snap({ queue: ["https://x/y"], trackId: "https://x/y", index: 0 }), NOW)).toBeNull();
  });
  test("only app-internal paths survive as the source", () => {
    expect(cleanSource({ label: "New", href: "/new" })).toEqual({ label: "New", href: "/new" });
    expect(cleanSource({ label: "Lib", href: "/?status=draft" })).not.toBeNull();
    for (const href of ["https://evil.example/", "//evil.example", "javascript:alert(1)", "new", "/a b", "/x://y"]) {
      expect(cleanSource({ label: "x", href })).toBeNull();
    }
    expect(cleanSource({ label: "", href: "/new" })).toBeNull();
    expect(cleanSource(null)).toBeNull();
  });
  test("a long label is cut", () => {
    expect(cleanSource({ label: "x".repeat(300), href: "/n" })!.label).toHaveLength(80);
  });
});

describe("storage failures are swallowed", () => {
  test("setItem throwing (quota, private mode) is a false, not an exception", () => {
    const s: KV = { getItem: () => null, setItem: () => { throw new Error("quota"); }, removeItem: () => {} };
    expect(save(s, snap(), NOW)).toBe(false);
  });
  test("getItem throwing loads nothing", () => {
    const s: KV = { getItem: () => { throw new Error("denied"); }, setItem: () => {}, removeItem: () => {} };
    expect(load(s, NOW)).toBeNull();
  });
});

// --- installPersistence and the boot restore against a real Player ---------------------------------------

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

function rig() {
  const el = new FakeAudio();
  const fetched: string[] = [];
  const device: DeviceHooks = { quality: () => "lossless", connection: () => undefined, recordFailedProbe() {} };
  const p = new Player({
    fetchLinks: async (id) => {
      fetched.push(id);
      return links(id);
    },
    device,
    now: () => Date.now(),
    fetchMeta: async (id) => ({ title: `Title ${id}`, album: "" }),
  });
  p.attach(el as unknown as HTMLAudioElement);
  const store = kv();
  const doc = new EventTarget() as EventTarget & { visibilityState: string };
  doc.visibilityState = "visible";
  const win = new EventTarget();
  const timers: { fn: () => void; ms: number }[] = [];
  let now = NOW;
  const env: PersistEnv = {
    kv: store,
    doc: doc as unknown as Document,
    win: win as unknown as Window,
    now: () => now,
    setInterval: (fn, ms) => (timers.push({ fn, ms }), timers.length),
    clearInterval: () => {},
  };
  const flush = () => new Promise((r) => setTimeout(r, 0));
  return { p, el, store, doc, win, timers, env, fetched, flush, tick: (ms: number) => (now += ms) };
}

const saved = (store: KV) => JSON.parse(store.getItem(STORAGE_KEY) ?? "null") as { trackId: string; position: number; queue: string[] } | null;

describe("installPersistence", () => {
  test("saves on track change, pause, hidden, pagehide and every 5 s while playing", async () => {
    const r = rig();
    installPersistence(r.p, r.env);
    expect(saved(r.store)).toBeNull(); // nothing loaded: nothing saved
    r.p.play("b", ["a", "b", "c"]);
    await r.flush();
    expect(saved(r.store)?.trackId).toBe("b"); // track change (mm:state)

    r.store.removeItem(STORAGE_KEY);
    r.el.currentTime = 10;
    r.p.state.current = 10;
    r.p.pause();
    expect(saved(r.store)?.position).toBe(10); // pause

    r.store.removeItem(STORAGE_KEY);
    r.doc.visibilityState = "hidden";
    r.doc.dispatchEvent(new Event("visibilitychange"));
    expect(saved(r.store)?.trackId).toBe("b"); // hidden
    r.doc.visibilityState = "visible";

    r.store.removeItem(STORAGE_KEY);
    r.doc.dispatchEvent(new Event("visibilitychange"));
    expect(saved(r.store)).toBeNull(); // visible is not a save point

    r.win.dispatchEvent(new Event("pagehide"));
    expect(saved(r.store)?.trackId).toBe("b"); // pagehide

    r.store.removeItem(STORAGE_KEY);
    expect(r.timers[0]?.ms).toBe(SAVE_EVERY_MS);
    r.timers[0]!.fn();
    expect(saved(r.store)).toBeNull(); // paused: the interval does not write
    r.p.resume();
    await r.flush();
    r.store.removeItem(STORAGE_KEY);
    r.timers[0]!.fn();
    expect(saved(r.store)?.trackId).toBe("b"); // playing: it does
  });

  test("restore comes back paused with the saved position, fetches only the current link, and one tap plays", async () => {
    const a = rig();
    installPersistence(a.p, a.env);
    a.p.play("b", ["a", "b", "c", "d"], { source: { label: "New", href: "/new" } });
    await a.flush();
    a.p.state.current = 72;
    a.p.pause();
    const raw = a.store.getItem(STORAGE_KEY)!;
    expect(raw).not.toMatch(/\/media\/|sig|exp/);

    // a fresh page life
    const b = rig();
    b.store.setItem(STORAGE_KEY, raw);
    const v = load(b.store, NOW + 60_000)!;
    expect(b.p.restore(v)).toBe(true);
    await b.flush();
    expect(b.p.state.trackId).toBe("b");
    expect(b.p.state.queue).toEqual(["a", "b", "c", "d"]);
    expect(b.p.state.index).toBe(1);
    expect(b.p.state.source).toEqual({ label: "New", href: "/new" });
    expect(b.p.state.restored).toBe(72);
    expect(b.p.state.playing).toBe(false);
    expect(b.p.playIntent).toBe(false);
    expect(b.el.paused).toBe(true);
    expect(b.el.src).toBe(""); // nothing applied, nothing autoplayed
    expect(b.fetched).toEqual(["b"]); // the link is minted ahead; no other media
    expect(b.p.title).toBe("Title b");

    b.p.resume();
    await b.flush();
    expect(b.el.src).toContain("/media/b.");
    expect(b.el.paused).toBe(false);
    expect(b.p.state.restored).toBeNull();
    // the cached link was used inside the tap: b is not minted twice (c is the usual next-track prefetch)
    expect(b.fetched).toEqual(["b", "c"]);
  });

  test("a restored, not yet resumed player does not refresh its savedAt", async () => {
    const r = rig();
    r.p.restore({ v: 1, queue: ["a", "b"], index: 0, queuedNext: 0, position: 5, trackId: "a", source: null, savedAt: NOW - 1000 });
    installPersistence(r.p, r.env);
    r.win.dispatchEvent(new Event("pagehide"));
    expect(saved(r.store)).toBeNull();
  });

  test("restore refuses when something is already loaded", () => {
    const r = rig();
    r.p.play("a", ["a"]);
    expect(r.p.restore({ v: 1, queue: ["x"], index: 0, queuedNext: 0, position: 0, trackId: "x", source: null, savedAt: NOW })).toBe(false);
    expect(r.p.state.trackId).toBe("a");
  });
});

describe("pre-warm on visible", () => {
  test("a paused track gets its link minted into the cache; a playing track is never touched", async () => {
    const r = rig();
    r.p.restore({ v: 1, queue: ["a", "b"], index: 0, queuedNext: 0, position: 3, trackId: "a", source: null, savedAt: NOW });
    await r.flush();
    expect(r.fetched).toEqual(["a"]);
    r.p.prewarmPaused();
    await r.flush();
    expect(r.fetched).toEqual(["a"]); // cached: nothing to do
    r.p.resume();
    await r.flush();
    const src = r.el.src;
    r.p.prewarmPaused();
    await r.flush();
    expect(r.fetched).toEqual(["a", "b"]); // b: the usual next-track prefetch, a is not minted again
    expect(r.el.src).toBe(src); // never swaps src
  });
});
