import { expect, test } from "bun:test";
import { Glob } from "bun";
import { readFileSync } from "node:fs";
import { join } from "node:path";

test("no Unicode media glyphs remain in web/src/**/*.svelte (icons are inline SVG)", () => {
  const root = join(import.meta.dirname, "..");
  const hits: string[] = [];
  for (const f of new Glob("**/*.svelte").scanSync({ cwd: root })) {
    const text = readFileSync(join(root, f), "utf8");
    if (/[⏮⏭⏸▶⤵]/u.test(text)) hits.push(f);
  }
  expect(hits).toEqual([]);
});
