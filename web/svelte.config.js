import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";

export default {
  preprocess: vitePreprocess(),
  // Runes only for project files, so Svelte 4 libraries in node_modules keep compiling.
  vitePlugin: {
    dynamicCompileOptions({ filename }) {
      if (!filename.includes("node_modules")) return { runes: true };
    },
  },
};
