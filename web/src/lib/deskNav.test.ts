import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { matchRoute, APP_ROUTES } from "./router";
import { CONTEXTUAL_ROUTES, NAV_LINKS, linkRoute, linkVisible, routeAllowed, routeOp, tabFor, titleFor } from "./nav";
import { PENDING_UI } from "../../e2e/coverage";

const owner = () => true;
// The Editor of the role matrix reads and edits plans; the Listener reaches none (D-DESK-14).
const editor = (op: string) => ["list_plans", "get_plan", "check_abc", "create_plan_from_take", "edit_plan", "search_tracks", "get_track"].includes(op);
const listener = (op: string) => ["search_tracks", "get_track", "get_song"].includes(op);

describe("the Desk routes", () => {
  test("/desk and /desk/plans/:id resolve, specific before parametric", () => {
    expect(matchRoute(APP_ROUTES, "/desk")?.route.name).toBe("desk");
    const m = matchRoute(APP_ROUTES, "/desk/plans/abc");
    expect(m?.route.name).toBe("plan");
    expect(m?.params.id).toBe("abc");
  });
  test("each page is built on its read op, under More, and a plan is reached from a page", () => {
    expect(routeOp("desk")).toBe("list_plans");
    expect(routeOp("plan")).toBe("get_plan");
    expect(tabFor("desk")).toBe("more");
    expect(tabFor("plan")).toBe("more");
    expect(titleFor("desk")).toBe("Desk");
    expect(CONTEXTUAL_ROUTES.has("plan")).toBe(true);
  });
  test("a Desk row sits in More under Make", () => {
    const l = NAV_LINKS.find((x) => x.path === "/desk");
    expect(l?.group).toBe("Make");
    expect(linkRoute(l!)).toBe("desk");
  });
});

describe("who sees the Desk", () => {
  const link = NAV_LINKS.find((x) => x.path === "/desk")!;
  test("the Owner and an Editor see the row and open both pages", () => {
    for (const can of [owner, editor]) {
      expect(linkVisible(link, can)).toBe(true);
      expect(routeAllowed("desk", can)).toBe(true);
      expect(routeAllowed("plan", can)).toBe(true);
    }
  });
  test("a Listener sees no row and is refused both pages", () => {
    expect(linkVisible(link, listener)).toBe(false);
    expect(routeAllowed("desk", listener)).toBe(false);
    expect(routeAllowed("plan", listener)).toBe(false);
  });
});

describe("coverage bookkeeping", () => {
  test("the six Desk ops are served by pages now, so none stays in PENDING_UI", () => {
    for (const id of ["list_plans", "get_plan", "check_abc", "create_plan_from_take", "edit_plan", "set_plan_verdict"]) expect(PENDING_UI.has(id), id).toBe(false);
  });
  test("every Desk op is called by an e2e spec of this lane", () => {
    const spec = readFileSync(resolve(import.meta.dirname, "../../e2e/desk-w1.spec.ts"), "utf8");
    for (const part of ["/api/plans", "/plan", "/verdict", "/edits", "/api/abc/check"]) expect(spec).toContain(part);
  });
});
