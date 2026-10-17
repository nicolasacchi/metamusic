import { describe, expect, test } from "bun:test";
import { isServerPath, matchRoute, parseLocation, type Route } from "./router";

const routes: Route[] = [
  { pattern: "/", name: "home" },
  { pattern: "/settings", name: "settings" },
  { pattern: "/tracks/:id", name: "track" },
  { pattern: "/files/*rest", name: "files" },
];

describe("parseLocation", () => {
  test("splits path and query", () => {
    expect(parseLocation("/a/b?x=1&y=two&x=3")).toEqual({
      path: "/a/b",
      query: { x: "3", y: "two" },
    });
  });
  test("drops hash and trailing slash", () => {
    expect(parseLocation("/settings/#top")).toEqual({ path: "/settings", query: {} });
    expect(parseLocation("/")).toEqual({ path: "/", query: {} });
  });
  test("decodes query values", () => {
    expect(parseLocation("/?q=a%20b+c").query).toEqual({ q: "a b c" });
  });
});

describe("matchRoute", () => {
  test("static routes", () => {
    expect(matchRoute(routes, "/")?.route.name).toBe("home");
    expect(matchRoute(routes, "/settings")?.route.name).toBe("settings");
  });
  test("params are decoded", () => {
    const m = matchRoute(routes, "/tracks/a%20b");
    expect(m?.route.name).toBe("track");
    expect(m?.params).toEqual({ id: "a b" });
  });
  test("wildcard captures rest", () => {
    expect(matchRoute(routes, "/files/a/b/c")?.params).toEqual({ rest: "a/b/c" });
  });
  test("no match returns null", () => {
    expect(matchRoute(routes, "/nope")).toBeNull();
    expect(matchRoute(routes, "/tracks")).toBeNull();
    expect(matchRoute(routes, "/tracks/1/extra")).toBeNull();
    expect(matchRoute(routes, "/settingsx")).toBeNull();
  });
  test("malformed escapes do not throw", () => {
    expect(matchRoute(routes, "/tracks/%E0%A4%A")?.params.id).toBe("%E0%A4%A");
  });
});

describe("isServerPath", () => {
  test("server-owned prefixes are not SPA routes", () => {
    for (const p of ["/api/docs", "/human/api/x", "/auth/login", "/mcp", "/healthz", "/__e2e/x", "/.well-known/a", "/media/x", "/icons/a.png", "/manifest.webmanifest"]) {
      expect(isServerPath(p)).toBe(true);
    }
    for (const p of ["/", "/settings", "/apix", "/mcpx", "/mediax", "/manifest.webmanifestx"]) expect(isServerPath(p)).toBe(false);
  });
});

describe("human and pairing pages", () => {
  test("/human (the hub; /human/ is stripped to it), /start and /pair are server paths, never client-routed", () => {
    for (const p of ["/human", "/human/tokens", "/human/devices", "/start", "/pair", "/pair/x"]) expect(isServerPath(p)).toBe(true);
    for (const p of ["/humane", "/started", "/pairs"]) expect(isServerPath(p)).toBe(false);
  });
});

import { APP_ROUTES } from "./router";

describe("APP_ROUTES (C40)", () => {
  const cases: [string, string, Record<string, string>?][] = [
    ["/", "home"],
    ["/tracks/abc", "track", { id: "abc" }],
    ["/backlog", "backlog"],
    ["/backlog/new", "draft-new"],
    ["/backlog/upload", "draft-upload"],
    ["/backlog/d1", "draft", { id: "d1" }],
    ["/playlists", "playlists"],
    ["/playlists/p1", "playlist", { id: "p1" }],
    ["/export", "export"],
    ["/import", "import"],
    ["/settings", "settings"],
    ["/settings/backups", "settings-backups"],
    ["/settings/audit", "settings-audit"],
    ["/human", "human"],
    ["/human/tokens", "human-tokens"],
    ["/human/devices", "human-devices"],
    ["/duels", "duels"],
    ["/duels/d1", "duel", { id: "d1" }],
    ["/ladder", "ladder"],
    ["/compare", "compare"],
  ];
  for (const [path, name, params] of cases) {
    test(path, () => {
      const m = matchRoute(APP_ROUTES, path);
      expect(m?.route.name).toBe(name);
      if (params) expect(m?.params).toEqual(params);
    });
  }
  test("unknown paths are 404", () => {
    expect(matchRoute(APP_ROUTES, "/humane")).toBeNull();
    expect(matchRoute(APP_ROUTES, "/nope")).toBeNull();
  });
});

describe("M3 routes (C84)", () => {
  test("seven new routes match, specific before parametric", async () => {
    const { APP_ROUTES } = await import("./router");
    const name = (p: string) => matchRoute(APP_ROUTES, p)?.route.name;
    expect(name("/jobs")).toBe("jobs");
    expect(name("/jobs/j1")).toBe("job");
    expect(name("/batches/b1")).toBe("batch");
    expect(name("/lineage/t1")).toBe("lineage");
    expect(name("/human/generate")).toBe("human-generate");
    expect(name("/human/confirm/b1")).toBe("human-confirm");
    expect(matchRoute(APP_ROUTES, "/human/confirm/b1")?.params).toEqual({ batch: "b1" });
    expect(name("/human/spend")).toBe("human-spend");
  });
});

describe("M4a routes (C115)", () => {
  test("/offline is an SPA route; connectors and subsonic are /human pages", () => {
    const name = (p: string) => matchRoute(APP_ROUTES, p)?.route.name;
    expect(name("/offline")).toBe("offline");
    expect(name("/human/connectors")).toBe("human-connectors");
    expect(name("/human/subsonic")).toBe("human-subsonic");
    expect(isServerPath("/human/connectors")).toBe(true);
    expect(isServerPath("/human/subsonic")).toBe(true);
    expect(isServerPath("/offline")).toBe(false);
  });
  test("the worker script and the Subsonic API are server paths", () => {
    for (const p of ["/sw.js", "/rest/ping.view"]) expect(isServerPath(p)).toBe(true);
  });
});

describe("L1 routes", () => {
  test("the four new routes match, specific before parametric", async () => {
    const { APP_ROUTES } = await import("./router");
    const name = (p: string) => matchRoute(APP_ROUTES, p)?.route.name;
    expect(name("/board")).toBe("board");
    expect(name("/muses/m1")).toBe("muse");
    expect(matchRoute(APP_ROUTES, "/muses/m1")?.params).toEqual({ id: "m1" });
    expect(name("/songs/s1")).toBe("song");
    expect(name("/sounds")).toBe("sounds");
    expect(name("/muses")).toBeUndefined();
    expect(name("/songs")).toBeUndefined();
    expect(isServerPath("/board")).toBe(false);
  });
});

import { createScrollKeeper, isSheetEntry, READY_TIMEOUT_MS, routeReady, type ScrollDeps } from "./scrollRestore";

// A fake history entry list plus a fake clock: the keeper only touches these.
function scrollWorld() {
  const entries: unknown[] = [null];
  let at = 0;
  let y = 0;
  const timers = new Map<number, { fn: () => void; at: number }>();
  let tid = 0;
  let clock = 0;
  let keys = 0;
  const deps: ScrollDeps = {
    history: {
      get state() {
        return entries[at];
      },
      replaceState(s) {
        entries[at] = s;
      },
    },
    scrollY: () => y,
    scrollTo: (v) => void (y = v),
    setTimer: (fn, ms) => {
      timers.set(++tid, { fn, at: clock + ms });
      return tid;
    },
    clearTimer: (h) => void timers.delete(h as number),
    newKey: () => `k${++keys}`,
  };
  const keeper = createScrollKeeper(deps);
  return {
    keeper,
    entries,
    get y() {
      return y;
    },
    set y(v: number) {
      y = v;
    },
    state: () => entries[at] as Record<string, unknown>,
    push(s: unknown) {
      entries.length = at + 1;
      entries.push(s);
      at++;
    },
    go(i: number) {
      at = i;
    },
    advance(ms: number) {
      clock += ms;
      for (const [id, t] of [...timers]) if (t.at <= clock) {
        timers.delete(id);
        t.fn();
      }
    },
  };
}

describe("scroll restoration (P0-9)", () => {
  test("the first entry gets a key; scrolling saves its position (debounced)", () => {
    const w = scrollWorld();
    w.keeper.adopt();
    expect(w.state().mmKey).toBe("k1");
    w.y = 1672;
    w.keeper.onScroll();
    expect(w.state().mmScroll).toBeUndefined();
    w.advance(200);
    expect(w.state().mmScroll).toBe(1672);
  });

  test("leaving saves at once; the new entry starts at the top; Back restores the saved position once ready", () => {
    const w = scrollWorld();
    w.keeper.adopt();
    w.y = 1672;
    const next = w.keeper.leave(); // navigate to a take
    expect(w.entries[0]).toMatchObject({ mmKey: "k1", mmScroll: 1672 });
    w.push(next);
    w.keeper.arrivedNew();
    expect(w.y).toBe(0);
    w.y = 300;
    w.keeper.saveNow(); // the take page, scrolled a little
    // Back
    w.go(0);
    expect(w.keeper.arrivedPopped(true)).toBe(true);
    w.y = 0; // the list is empty while it loads
    w.keeper.onScroll();
    w.advance(500);
    expect(w.state().mmScroll).toBe(1672); // the clamp to 0 must not overwrite the saved position
    w.keeper.ready();
    expect(w.y).toBe(1672);
    // Forward again keeps the take's own position
    w.go(1);
    w.keeper.arrivedPopped(true);
    w.keeper.ready();
    expect(w.y).toBe(300);
  });

  test("a route that never signals is restored by the timeout, an unwired one shortly after render", () => {
    const w = scrollWorld();
    w.keeper.adopt();
    w.y = 900;
    w.push(w.keeper.leave());
    w.keeper.arrivedNew();
    w.go(0);
    w.keeper.arrivedPopped(true);
    w.advance(READY_TIMEOUT_MS - 1);
    expect(w.y).toBe(0);
    w.advance(1);
    expect(w.y).toBe(900);
    w.go(1);
    w.y = 0;
    w.go(0);
    w.keeper.arrivedPopped(false);
    w.advance(299);
    expect(w.y).toBe(0);
    w.advance(1);
    expect(w.y).toBe(900);
  });

  test("the user scrolling by hand cancels a pending restore", () => {
    const w = scrollWorld();
    w.keeper.adopt();
    w.y = 500;
    w.push(w.keeper.leave());
    w.go(0);
    w.keeper.arrivedPopped(true);
    w.keeper.abandon();
    w.keeper.ready();
    expect(w.y).toBe(500); // untouched (this fake does not reset y on navigation)
    expect(w.keeper.restoring).toBe(false);
  });

  test("an entry without a saved position is left alone", () => {
    const w = scrollWorld();
    w.keeper.adopt();
    w.push({ mmKey: "x" });
    w.y = 40;
    w.keeper.arrivedPopped(true);
    expect(w.keeper.restoring).toBe(false);
    expect(w.y).toBe(40);
  });

  test("now-playing sheet entries are never touched or restored", () => {
    const w = scrollWorld();
    w.keeper.adopt();
    const sheet = { mmSheet: true };
    w.push(sheet);
    expect(isSheetEntry(w.state())).toBe(true);
    w.y = 700;
    w.keeper.onScroll();
    w.keeper.saveNow();
    w.advance(500);
    expect(w.entries[1]).toBe(sheet); // same object: never rewritten
    expect(w.keeper.arrivedPopped(true)).toBe(false);
    w.keeper.adopt();
    expect(w.entries[1]).toBe(sheet);
  });

  test("an entry pushed by another module (null state) is adopted on the next save", () => {
    const w = scrollWorld();
    w.keeper.adopt();
    w.push(null);
    w.y = 12;
    w.keeper.saveNow();
    expect(w.state()).toEqual({ mmScroll: 12 });
  });

  test("routeReady dispatches mm:route-ready", async () => {
    const seen: string[] = [];
    const g = globalThis as { window?: unknown };
    const prev = g.window;
    g.window = { dispatchEvent: (e: Event) => void seen.push(e.type) };
    try {
      await routeReady();
    } finally {
      g.window = prev;
    }
    expect(seen).toEqual(["mm:route-ready"]);
  });
});
