import { defineConfig } from "eslint/config";
import js from "@eslint/js";
import ts from "typescript-eslint";
import svelte from "eslint-plugin-svelte";
import globals from "globals";

const banned = ["fetch", "XMLHttpRequest", "EventSource", "indexedDB", "caches"];

export default defineConfig(
  { ignores: ["dist/", "node_modules/", "src/api/schema.d.ts", "playwright-report/", "test-results/"] },
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs["flat/recommended"],
  {
    languageOptions: { globals: { ...globals.browser } },
  },
  {
    files: ["**/*.svelte", "**/*.svelte.ts"],
    languageOptions: { parserOptions: { parser: ts.parser } },
  },
  {
    // Lyrics, titles and styles render as text; never raw HTML (set explicitly so a config change cannot drop it).
    files: ["**/*.svelte"],
    rules: { "svelte/no-at-html-tags": "error" },
  },
  {
    // Network access only through src/api/ (architecture rule 2).
    rules: {
      "no-restricted-globals": [
        "error",
        ...banned.map((name) => ({ name, message: "Network access is only allowed in src/api/." })),
      ],
      "no-restricted-properties": [
        "error",
        ...["window", "globalThis", "self"].flatMap((object) =>
          banned.map((property) => ({ object, property, message: "Network access is only allowed in src/api/." })),
        ),
        { object: "navigator", property: "sendBeacon", message: "Network access is only allowed in src/api/." },
      ],
      "no-restricted-syntax": [
        "error",
        {
          selector: "MemberExpression[property.name='sendBeacon']",
          message: "Network access is only allowed in src/api/.",
        },
        {
          selector: "NewExpression[callee.name=/^(XMLHttpRequest|EventSource)$/]",
          message: "Network access is only allowed in src/api/.",
        },
      ],
    },
  },
  {
    files: ["src/api/**", "e2e/**", "playwright.config.ts", "vite.config.ts", "eslint.config.js", "svelte-bun.ts"],
    languageOptions: { globals: { ...globals.node } },
    rules: {
      "no-restricted-globals": "off",
      "no-restricted-properties": "off",
      "no-restricted-syntax": "off",
    },
  },
);
