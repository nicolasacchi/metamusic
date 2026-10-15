import { resolve } from "node:path";
import { defineConfig } from "vite";

// Second build, run after the main one: the service worker as a classic (IIFE) script at dist/sw.js.
// emptyOutDir must stay false: it would wipe the main build and dist/.gitkeep.
export default defineConfig({
  publicDir: false,
  build: {
    outDir: "dist",
    emptyOutDir: false,
    sourcemap: false,
    lib: { entry: resolve(import.meta.dirname, "src/api/sw/sw.ts"), formats: ["iife"], name: "mmSw", fileName: () => "sw.js" },
  },
});
