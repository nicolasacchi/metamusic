import { describe, expect, test } from "bun:test";
import { buildPrecache, injectPrecache } from "../../../scripts/precache";

const manifest = {
  "index.html": { file: "assets/index-abc.js", css: ["assets/index-def.css"], assets: ["assets/font-1.woff2"], isEntry: true },
  "src/x.svelte": { file: "assets/x-123.js", imports: ["index.html"] },
};

describe("buildPrecache", () => {
  test("lists index.html and every emitted asset, sorted, with a stable version", () => {
    const a = buildPrecache(manifest);
    expect(a.urls).toEqual(["/assets/font-1.woff2", "/assets/index-abc.js", "/assets/index-def.css", "/assets/x-123.js", "/index.html"]);
    expect(a.version).toMatch(/^[0-9a-f]{12}$/);
    expect(buildPrecache(manifest)).toEqual(a);
  });
  test("a different asset changes the version", () => {
    expect(buildPrecache({ ...manifest, "src/y.svelte": { file: "assets/y-9.js" } }).version).not.toBe(buildPrecache(manifest).version);
  });
  test("never lists API paths", () => {
    expect(JSON.stringify(buildPrecache(manifest))).not.toContain("/api/");
  });
});

describe("injectPrecache", () => {
  test("replaces the quoted token", () => {
    const out = injectPrecache('const p="__MM_PRECACHE__";run(p)', { version: "v", urls: ["/a"] });
    expect(out).toBe('const p={"version":"v","urls":["/a"]};run(p)');
  });
  test("throws when the token is missing", () => {
    expect(() => injectPrecache("nothing", { version: "v", urls: [] })).toThrow();
  });
});
