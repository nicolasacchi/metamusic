// bun test preload: compile *.svelte.ts / *.svelte.js modules (runes) with the Svelte compiler.
import { plugin } from "bun";
import { compileModule } from "svelte/compiler";
import { readFileSync } from "node:fs";

plugin({
  name: "svelte-runes-module",
  setup(build) {
    build.onLoad({ filter: /\.svelte\.(ts|js)$/ }, async ({ path }) => {
      let src = readFileSync(path, "utf8");
      if (path.endsWith(".ts")) src = new Bun.Transpiler({ loader: "ts" }).transformSync(src);
      const out = compileModule(src, { filename: path, generate: "client" });
      return { contents: out.js.code, loader: "js" };
    });
  },
});
