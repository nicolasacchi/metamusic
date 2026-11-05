import { expect, test } from "bun:test";
import { APP_ROUTES } from "./router";
import { NAV_LINKS, tabFor, titleFor } from "./nav";

test("E2: /engines sits under More beside Transforms; trial and suite pages are contextual", () => {
  expect(APP_ROUTES.find((r) => r.name === "engines")?.pattern).toBe("/engines");
  expect(APP_ROUTES.find((r) => r.name === "engine-trial")?.pattern).toBe("/engines/trials/:id");
  expect(APP_ROUTES.find((r) => r.name === "suite")?.pattern).toBe("/suites/:id");
  expect(tabFor("engines")).toBe("more");
  expect(tabFor("engine-trial")).toBe("more");
  expect(tabFor("suite")).toBe("library");
  expect(titleFor("engines")).toBe("Engines");
  expect(NAV_LINKS.find((l) => l.path === "/engines")).toMatchObject({ label: "Engines", tab: "more", group: "Make" });
});
