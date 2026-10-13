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
  test("/human, /start and /pair are server paths, never client-routed", () => {
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
    expect(matchRoute(APP_ROUTES, "/human")).toBeNull();
    expect(matchRoute(APP_ROUTES, "/nope")).toBeNull();
  });
});
