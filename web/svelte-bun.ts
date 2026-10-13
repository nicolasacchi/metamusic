// bun test preload: compile *.svelte.ts / *.svelte.js modules (runes) with the Svelte compiler.
import { plugin } from "bun";
import { compile, compileModule } from "svelte/compiler";
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

    // Components compile for SSR so unit tests can render them with svelte/server (no DOM needed).
    build.onLoad({ filter: /\.svelte$/ }, async ({ path }) => {
      const out = compile(readFileSync(path, "utf8"), { filename: path, generate: "server", runes: true });
      return { contents: out.js.code, loader: "js" };
    });
  },
});
