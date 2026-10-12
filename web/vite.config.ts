import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

export default defineConfig({
  plugins: [svelte()],
  build: { outDir: "dist", emptyOutDir: false, sourcemap: false },
  server: {
    proxy: { "/api": "http://127.0.0.1:8080", "/human": "http://127.0.0.1:8080" },
  },
});
