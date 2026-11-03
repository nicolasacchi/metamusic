import { expect, test } from "bun:test";
import { APP_ROUTES } from "./router";
import { NAV_LINKS, tabFor, titleFor } from "./nav";

test("T1: /transforms is a More route beside Jobs, not a Library sub-view or a new tab", () => {
  expect(APP_ROUTES.find((r) => r.name === "transforms")?.pattern).toBe("/transforms");
  expect(tabFor("transforms")).toBe("more");
  expect(titleFor("transforms")).toBe("Transforms");
  const jobs = NAV_LINKS.findIndex((l) => l.path === "/jobs");
  const tr = NAV_LINKS.findIndex((l) => l.path === "/transforms");
  expect(tr).toBe(jobs + 1);
  expect(NAV_LINKS[tr]).toMatchObject({ label: "Transforms", tab: "more", group: "Make" });
  expect(NAV_LINKS[tr]?.secure).toBeUndefined();
});
