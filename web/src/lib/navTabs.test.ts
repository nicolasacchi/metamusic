import { expect, test } from "bun:test";
import { APP_ROUTES } from "./router";
import { CONTEXTUAL_ROUTES, NAV_LINKS, TABS, isSecureRoute, mappedRouteNames, tabFor, titleFor } from "./nav";

test("every APP_ROUTES entry maps to exactly one tab, and nothing else is mapped", () => {
  const names = APP_ROUTES.map((r) => r.name);
  expect(new Set(names).size).toBe(names.length);
  expect(mappedRouteNames().sort()).toEqual([...names].sort());
  const tabs = new Set(TABS.map((t) => t.id));
  for (const n of names) expect(tabs.has(tabFor(n)), n).toBe(true);
});

test("the listen-first tabs", () => {
  expect(TABS.map((t) => t.label)).toEqual(["New", "Library", "Play queue", "Judge", "More"]);
  expect(tabFor("home")).toBe("library");
  expect(tabFor("queue")).toBe("queue");
  expect(tabFor("duel")).toBe("judge");
  expect(tabFor("board")).toBe("more"); // Make lives under More
  expect(tabFor("nope")).toBe("more");
});

test("/human routes are flagged secure and so are their More rows", () => {
  for (const r of APP_ROUTES) expect(isSecureRoute(r.name), r.name).toBe(r.pattern.startsWith("/human"));
  for (const l of NAV_LINKS) expect(!!l.secure, l.label).toBe(l.path.startsWith("/human"));
});

test("every static route has a nav link or is reached from a page; each link sits in its route's tab", () => {
  for (const r of APP_ROUTES) {
    if (r.pattern.includes(":") || CONTEXTUAL_ROUTES.has(r.name)) continue;
    const link = NAV_LINKS.find((l) => (l.path.replace(/\/$/, "") || "/") === r.pattern);
    if (r.name === "new" || r.name === "queue" || r.name === "more") continue; // the tabs themselves
    expect(link, r.pattern).toBeDefined();
    expect(link?.tab, r.pattern).toBe(tabFor(r.name));
  }
});

test("every route has a title", () => {
  for (const r of APP_ROUTES) expect(titleFor(r.name), r.name).not.toBe("Not found");
});
