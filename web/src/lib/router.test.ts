import { describe, expect, test } from "bun:test";
import { matchRoute, parseLocation, type Route } from "./router";

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
