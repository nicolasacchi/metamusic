import { expect, test } from "bun:test";
import { Glob } from "bun";
import { readFileSync } from "node:fs";
import { join } from "node:path";

test("exactly one <audio element exists in web/src, in App.svelte", () => {
  const hits: string[] = [];
  for (const f of new Glob("**/*.{svelte,ts}").scanSync({ cwd: import.meta.dirname })) {
    if (f.endsWith(".test.ts")) continue;
    const text = readFileSync(join(import.meta.dirname, f), "utf8");
    const n = text.match(/<audio\b/g)?.length ?? 0;
    for (let i = 0; i < n; i++) hits.push(f);
  }
  expect(hits).toEqual(["App.svelte"]);
});

test("App.svelte keeps the audio element in the footer and renders it unconditionally", () => {
  const app = readFileSync(join(import.meta.dirname, "App.svelte"), "utf8");
  const footer = /<footer class="player"[^>]*>([\s\S]*?)<\/footer>/.exec(app);
  expect(footer).not.toBeNull();
  expect(footer![1]).toContain("<audio");
  expect(footer![1]).toContain("data-audio-id");
  expect(footer![1]).not.toMatch(/\{#(if|each|key)/);
});
