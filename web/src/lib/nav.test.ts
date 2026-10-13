import { describe, expect, test } from "bun:test";
import { createNav, isHumanPath } from "./nav";

function setup() {
  const assigned: string[] = [];
  const routed: string[] = [];
  const nav = createNav({ assign: (u) => void assigned.push(u), route: (u) => void routed.push(u) });
  return { nav, assigned, routed };
}

function click(href: string, over: Partial<MouseEvent> = {}) {
  let prevented = false;
  const e = { button: 0, defaultPrevented: false, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, preventDefault: () => (prevented = true), ...over } as unknown as MouseEvent;
  return { e, href, prevented: () => prevented };
}

describe("nav", () => {
  test("isHumanPath", () => {
    expect(isHumanPath("/human/tokens")).toBe(true);
    expect(isHumanPath("/human")).toBe(true);
    expect(isHumanPath("/humane")).toBe(false);
    expect(isHumanPath("/backlog")).toBe(false);
  });

  test("clicking a /human link calls location.assign and never the client router", () => {
    const { nav, assigned, routed } = setup();
    const c = click("/human/tokens");
    nav.onLinkClick(c.e, c.href);
    expect(c.prevented()).toBe(true);
    expect(assigned).toEqual(["/human/tokens"]);
    expect(routed).toEqual([]);
    const d = click("/human/devices");
    nav.onLinkClick(d.e, d.href);
    expect(assigned).toEqual(["/human/tokens", "/human/devices"]);
  });

  test("modified clicks are left to the browser", () => {
    const { nav, assigned } = setup();
    const c = click("/human/tokens", { ctrlKey: true });
    nav.onLinkClick(c.e, c.href);
    expect(c.prevented()).toBe(false);
    expect(assigned).toEqual([]);
  });

  test("navigate to /human/* uses assign; others use the router", () => {
    const { nav, assigned, routed } = setup();
    nav.navigate("/human/devices");
    nav.navigate("/backlog");
    expect(assigned).toEqual(["/human/devices"]);
    expect(routed).toEqual(["/backlog"]);
  });

  test("non-human link clicks are not handled", () => {
    const { nav, assigned } = setup();
    const c = click("/backlog");
    nav.onLinkClick(c.e, c.href);
    expect(c.prevented()).toBe(false);
    expect(assigned).toEqual([]);
  });
});
