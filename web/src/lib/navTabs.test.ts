import { expect, test } from "bun:test";
import { APP_ROUTES } from "./router";
import { CONTEXTUAL_ROUTES, NAV_LINKS, TABS, isSecureRoute, isTabRoot, mappedRouteNames, subActive, subLinks, tabFor, titleFor } from "./nav";

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

test("L2: Records and Crates are Library sub-views; curation lives under More", () => {
  for (const n of ["records", "record", "crates", "crate"]) expect(tabFor(n), n).toBe("library");
  for (const n of ["settings-curation", "human-record-publish", "human-curation"]) expect(tabFor(n), n).toBe("more");
  expect(subLinks("library").map((l) => l.label)).toEqual(["Takes", "Sounds", "Records", "Crates", "Playlists"]);
  const secure = NAV_LINKS.find((l) => l.path === "/human/curation");
  expect(secure).toMatchObject({ label: "Opt-ins", tab: "more", group: "Secure", secure: true });
  expect(NAV_LINKS.find((l) => l.path === "/settings/curation")).toMatchObject({ tab: "more", group: "Settings" });
  expect(NAV_LINKS.some((l) => l.path.includes("/publish"))).toBe(false);
});

test("L2: sub-view highlighting, tab roots, contextual routes and titles", () => {
  const link = (p: string) => NAV_LINKS.find((l) => l.path === p)!;
  expect(subActive(link("/records"), "record")).toBe(true);
  expect(subActive(link("/records"), "crates")).toBe(false);
  expect(subActive(link("/crates"), "crate")).toBe(true);
  expect(isTabRoot("records")).toBe(true);
  expect(isTabRoot("crates")).toBe(true);
  expect(isTabRoot("record")).toBe(false);
  for (const n of ["record", "crate", "human-record-publish"]) expect(CONTEXTUAL_ROUTES.has(n), n).toBe(true);
  for (const [n, t] of [["records", "Records"], ["record", "Record"], ["crates", "Crates"], ["crate", "Crate"], ["settings-curation", "Curation"], ["human-curation", "Curation"], ["human-record-publish", "Publish"]] as const) {
    expect(titleFor(n)).toBe(t);
  }
});
