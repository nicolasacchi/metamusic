import { describe, expect, test } from "bun:test";
import { applyUserState, canOp, fetchWhoami, identityOf, LAST_KEY, mustReset, readLast, roleOf, USER_KEY, writeLast, type KV, type Whoami } from "./me";

class MemKV implements KV {
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

function env(kv: KV) {
  const dropped: string[] = [];
  return {
    dropped,
    env: {
      kv,
      dropMediaCache: async () => void dropped.push("cache"),
      dropOfflineDb: async () => void dropped.push("db"),
      dropOutboxDb: async () => void dropped.push("outbox"),
    },
  };
}

const who = (o: Partial<Whoami> = {}): Whoami => ({
  kind: "human",
  name: "alice",
  role: "owner",
  user: "alice",
  scopes: ["read"],
  allowed_ops: ["whoami", "search_tracks"],
  step_up_until: null,
  via: "proxy",
  ...o,
});

describe("can", () => {
  test("only listed ops pass; unloaded callers can do nothing", () => {
    expect(canOp(["a", "b"], "a")).toBe(true);
    expect(canOp(["a", "b"], "c")).toBe(false);
    expect(canOp([], "a")).toBe(false);
    expect(canOp(null, "a")).toBe(false);
    expect(canOp(undefined, "a")).toBe(false);
  });
  test("roleOf accepts the three roles only", () => {
    expect(roleOf("owner")).toBe("owner");
    expect(roleOf("editor")).toBe("editor");
    expect(roleOf("listener")).toBe("listener");
    expect(roleOf(null)).toBeNull();
    expect(roleOf("root")).toBeNull();
  });
});

describe("per-user browser state reset (C313.7)", () => {
  test("mustReset: same user keeps, another user resets", () => {
    expect(mustReset("alice", "alice", "owner", true)).toBe(false);
    expect(mustReset("alice", "bob", "editor", true)).toBe(true);
    expect(mustReset("alice", "bob", "editor", false)).toBe(true);
  });
  test("no recorded user: legacy state is the Owner's, anybody else wipes it", () => {
    expect(mustReset(null, "alice", "owner", true)).toBe(false);
    expect(mustReset(null, "bob", "editor", true)).toBe(true);
    expect(mustReset(null, "carol", "listener", false)).toBe(false);
  });
  test("a different user clears every mm:* key, the media cache and the offline database, then records the user", async () => {
    const kv = new MemKV();
    kv.setItem(USER_KEY, "alice");
    kv.setItem("mm:player:v1", "{}");
    kv.setItem("mm:device-id", "d1");
    kv.setItem("mm:inbox-heard:d1", "[]");
    kv.setItem("other", "keep");
    const { env: e, dropped } = env(kv);
    expect(await applyUserState(e, "bob", "editor")).toBe(true);
    expect([...kv.m.keys()].sort()).toEqual(["mm:me:user", "other"]);
    expect(kv.getItem(USER_KEY)).toBe("bob");
    expect(dropped.sort()).toEqual(["cache", "db", "outbox"]);
  });
  test("the same user touches nothing", async () => {
    const kv = new MemKV();
    kv.setItem(USER_KEY, "alice");
    kv.setItem("mm:player:v1", "{}");
    const { env: e, dropped } = env(kv);
    expect(await applyUserState(e, "alice", "owner")).toBe(false);
    expect(kv.getItem("mm:player:v1")).toBe("{}");
    expect(dropped).toEqual([]);
  });
  test("the Owner adopts pre-U1 state and keeps it (offline downloads included)", async () => {
    const kv = new MemKV();
    kv.setItem("mm:player:v1", "{}");
    const { env: e, dropped } = env(kv);
    expect(await applyUserState(e, "alice", "owner")).toBe(false);
    expect(kv.getItem("mm:player:v1")).toBe("{}");
    expect(kv.getItem(USER_KEY)).toBe("alice");
    expect(dropped).toEqual([]);
  });
  test("a non-Owner finding unowned state clears it", async () => {
    const kv = new MemKV();
    kv.setItem("mm:player:v1", "{}");
    const { env: e, dropped } = env(kv);
    expect(await applyUserState(e, "carol", "listener")).toBe(true);
    expect(kv.getItem("mm:player:v1")).toBeNull();
    expect(dropped.sort()).toEqual(["cache", "db", "outbox"]);
  });
  test("the saved whoami alone is not 'state'; a failing cache drop never blocks", async () => {
    const kv = new MemKV();
    kv.setItem(LAST_KEY, "{}");
    const { env: e } = env(kv);
    expect(await applyUserState(e, "carol", "listener")).toBe(false);
    kv.setItem("mm:player:v1", "{}");
    const boom = { kv, dropMediaCache: () => Promise.reject(new Error("x")), dropOfflineDb: () => Promise.reject(new Error("y")), dropOutboxDb: () => Promise.reject(new Error("z")) };
    expect(await applyUserState(boom, "bob", "editor")).toBe(true);
    expect(kv.getItem(USER_KEY)).toBe("bob");
  });
  test("a principal with no user is keyed by kind and name, so it cannot inherit a user's state", async () => {
    const kv = new MemKV();
    kv.setItem(USER_KEY, "alice");
    kv.setItem("mm:player:v1", "{}");
    const { env: e, dropped } = env(kv);
    const id = identityOf(who({ user: null, kind: "device", name: "kiosk", role: null }));
    expect(id).toBe("device:kiosk");
    expect(await applyUserState(e, id, null)).toBe(true);
    expect(kv.getItem("mm:player:v1")).toBeNull();
    expect(dropped).toContain("outbox");
    expect(identityOf(who())).toBe("alice");
  });
});

describe("last whoami (offline start)", () => {
  test("round-trips and rejects malformed values", () => {
    const kv = new MemKV();
    expect(readLast(kv)).toBeNull();
    writeLast(kv, who());
    expect(readLast(kv)?.allowed_ops).toEqual(["whoami", "search_tracks"]);
    kv.setItem(LAST_KEY, "not json");
    expect(readLast(kv)).toBeNull();
    kv.setItem(LAST_KEY, JSON.stringify({ kind: "human" }));
    expect(readLast(kv)).toBeNull();
  });
  test("fetchWhoami: offline only when the request threw; an HTTP answer is never a fallback", async () => {
    expect(await fetchWhoami({ GET: () => Promise.reject(new Error("net")) })).toEqual({ who: null, offline: true });
    expect(await fetchWhoami({ GET: () => Promise.resolve({ error: {} }) })).toEqual({ who: null, offline: false });
    expect(await fetchWhoami({ GET: () => Promise.resolve({}) })).toEqual({ who: null, offline: false });
    expect((await fetchWhoami({ GET: () => Promise.resolve({ data: who() }) })).who?.name).toBe("alice");
  });
});

test("the reset names match the service worker's store", async () => {
  const { CACHE_NAME, DB_NAME } = await import("../api/sw/store");
  const { MEDIA_CACHE, OFFLINE_DB } = await import("./me");
  expect(MEDIA_CACHE).toBe(CACHE_NAME);
  expect(OFFLINE_DB).toBe(DB_NAME);
});

test("the outbox database name matches the outbox store", async () => {
  const { OUTBOX_DB } = await import("./me");
  const src = await Bun.file(new URL("../api/outbox.ts", import.meta.url)).text();
  expect(src).toContain("indexedDB.open(OUTBOX_DB");
  expect(OUTBOX_DB).toBe("mm-outbox");
});
