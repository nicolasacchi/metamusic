// Runs against the output of `bun run build`; skipped when dist/ was not built (MM_REQUIRE_SW_BUILD=1 makes that a failure).
import { describe, expect, test } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";

const dist = resolve(import.meta.dirname, "../../../dist");
const built = existsSync(resolve(dist, "sw.js"));
const maybe = built || process.env.MM_REQUIRE_SW_BUILD === "1" ? describe : describe.skip;

maybe("dist/sw.js (after bun run build)", () => {
  const sw = () => readFileSync(resolve(dist, "sw.js"), "utf8");
  test("exists and is a classic script", () => {
    expect(built).toBe(true);
    expect(sw()).not.toMatch(/^\s*(import|export)\s/m);
    expect(sw()).not.toMatch(/\bimport\s*\(/);
  });
  test("contains the injected precache list, no token left, and no API URL", () => {
    const text = sw();
    expect(text).not.toContain("__MM_PRECACHE__");
    const m = /\{"version":"([0-9a-f]{12})","urls":(\[[^\]]*\])\}/.exec(text);
    expect(m).not.toBeNull();
    const urls = JSON.parse(m![2]!) as string[];
    expect(urls).toContain("/index.html");
    expect(urls.some((u) => u.startsWith("/assets/"))).toBe(true);
    expect(text).not.toContain("/api/");
  });
  test("claims clients on activate", () => {
    expect(sw()).toContain("clients.claim");
  });
  test("the worker build did not empty dist", () => {
    for (const f of ["index.html", ".vite/manifest.json", ".gitkeep"]) expect(existsSync(resolve(dist, f))).toBe(true);
  });
});
