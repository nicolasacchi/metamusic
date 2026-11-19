import { beforeEach, describe, expect, test } from "bun:test";
import { RateLimited } from "../api/client";
import { BOOT_TIMEOUT_MS, bootMe, me } from "./me.svelte";
import { fetchWhoami, LAST_KEY, USER_KEY, type MeClient, type Whoami } from "./me";

class MemKV {
  m = new Map<string, string>();
  get length() {
    return this.m.size;
  }
  key(i: number) {
    return [...this.m.keys()][i] ?? null;
  }
  getItem(k: string) {
    return this.m.get(k) ?? null;
  }
  setItem(k: string, v: string) {
    this.m.set(k, v);
  }
  removeItem(k: string) {
    this.m.delete(k);
  }
}

const who = (o: Partial<Whoami> = {}): Whoami => ({
  kind: "human",
  name: "alice",
  role: "owner",
  user: "alice",
  via: "proxy",
  scopes: [],
  allowed_ops: ["search_tracks"],
  step_up_until: null,
  ...o,
} as Whoami);

let kv: MemKV;
beforeEach(() => {
  kv = new MemKV();
  (globalThis as { localStorage?: unknown }).localStorage = kv;
  me.loaded = false;
  me.stale = false;
  me.name = "";
});

// Like fetch: never answers, rejects when the signal aborts.
const hung: MeClient = {
  GET: (_p, init) =>
    new Promise((_res, rej) => {
      init.signal?.addEventListener("abort", () => rej(init.signal!.reason));
    }),
};

describe("bootMe boot timeout (Revision 8)", () => {
  test("the boot timeout is 3 s", () => {
    expect(BOOT_TIMEOUT_MS).toBe(3000);
  });
  test("whoami never answers: the stored shell is used, marked stale", async () => {
    kv.setItem(LAST_KEY, JSON.stringify(who({ name: "stored" })));
    const t0 = Date.now();
    expect(await bootMe(30, hung)).toBe(true);
    expect(Date.now() - t0).toBeLessThan(1000);
    expect(me.loaded).toBe(true);
    expect(me.stale).toBe(true);
    expect(me.name).toBe("stored");
  });
  test("whoami answers in time: the stored shell is not used", async () => {
    kv.setItem(LAST_KEY, JSON.stringify(who({ name: "stored" })));
    const live: MeClient = { GET: async () => ({ data: who({ name: "live" }) }) };
    expect(await bootMe(1000, live)).toBe(true);
    expect(me.stale).toBe(false);
    expect(me.name).toBe("live");
    expect(JSON.parse(kv.getItem(LAST_KEY)!).name).toBe("live");
    expect(kv.getItem(USER_KEY)).toBe("alice");
  });
  test("a timeout with no stored shell resolves false", async () => {
    expect(await bootMe(30, hung)).toBe(false);
    expect(me.loaded).toBe(false);
  });
});

describe("a 429 from /api/me is an answer, not an outage", () => {
  test("fetchWhoami: RateLimited is not offline", async () => {
    const c: MeClient = { GET: () => Promise.reject(new RateLimited(5)) };
    expect(await fetchWhoami(c)).toEqual({ who: null, offline: false });
  });
  test("fetchWhoami: any thrown error carrying a response is not offline", async () => {
    const c: MeClient = { GET: () => Promise.reject(Object.assign(new Error("x"), { response: new Response(null, { status: 502 }) })) };
    expect((await fetchWhoami(c)).offline).toBe(false);
  });
  test("bootMe: 429 never falls back to the stored shell (shows the retry line)", async () => {
    kv.setItem(LAST_KEY, JSON.stringify(who({ name: "stored" })));
    const c: MeClient = { GET: () => Promise.reject(new RateLimited(5)) };
    expect(await bootMe(1000, c)).toBe(false);
    expect(me.loaded).toBe(false);
    expect(me.stale).toBe(false);
  });
});
