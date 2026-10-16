import { writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// emptyOutDir wipes dist/.gitkeep (needed so `go build` works without Node); restore it.
function keepDistPlaceholder(): Plugin {
  return {
    name: "keep-dist-placeholder",
    apply: "build",
    closeBundle() {
      writeFileSync(resolve(import.meta.dirname, "dist/.gitkeep"), "");
    },
  };
}

export default defineConfig({
  plugins: [svelte(), keepDistPlaceholder()],
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false, manifest: true },
  server: {
    proxy: Object.fromEntries(
      ["/api", "/human", "/auth", "/healthz"].map((p) => [p, "http://127.0.0.1:8080"]),
    ),
  },
});
