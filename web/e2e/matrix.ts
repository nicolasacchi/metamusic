// Risk-based device matrix. Every spec file starts with one header line
//   // matrix: @mobile @firefox because <why>      (or: // matrix: chrome-only because <why>)
// and that line is the single source of truth for where the file runs:
//   (none)            desktop Chrome only (the default; pure data, API and admin flows)
//   @mobile           also Pixel 7 and iPhone 15 (layout, touch, player, shell/nav, PWA, approvals, human pages)
//   @firefox          also desktop Firefox (codecs, autoplay, audio-element behaviour)
//   @android-firefox  also the firefox-android projects (Android Firefox UA, phone viewport, strict autoplay)
//   @realtime         informational: the spec asserts on real-time playback (no extra device by itself)
// MM_E2E_MATRIX=full ignores the tags: every spec runs on every project (nightly job). The structural placement of
// playwright.config.ts (listen-* and m3-* phases, m3-spend alone) still applies, because it is about shared state.
//
// Quarantine: a test titled with @quarantine, with a "// quarantine: <reason + link>" comment in its file, is skipped
// by default; MM_E2E_QUARANTINE=1 adds it to the run, =only runs nothing else (the non-blocking CI job).
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export const KINDS = ["mobile", "firefox", "android-firefox"] as const;
export type Kind = (typeof KINDS)[number];
export const KNOWN_TAGS = new Set<string>([...KINDS, "realtime"]);
export const MAX_QUARANTINED = 5;

const dir = dirname(fileURLToPath(import.meta.url));
const HEADER = /^\/\/ matrix: (.*?) because (\S.*)$/;

export type Header = { tags: string[]; reason: string } | undefined;

/** Parses the `// matrix:` header out of the first lines of a spec source. */
export function parseHeader(src: string): Header {
  for (const line of src.split("\n", 12)) {
    const m = HEADER.exec(line);
    if (m?.[1] !== undefined && m[2] !== undefined) return { tags: (m[1].match(/@[a-z-]+/g) ?? []).map((t) => t.slice(1)), reason: m[2] };
  }
  return undefined;
}

export function specFiles(): string[] {
  return readdirSync(dir).filter((f) => f.endsWith(".spec.ts")).sort();
}

export function headerOf(file: string): Header {
  return parseHeader(readFileSync(join(dir, file), "utf8"));
}

export const isFull = () => process.env.MM_E2E_MATRIX === "full";

/** Spec files that do NOT run on the given device kind: a testIgnore list ([] in the full matrix). */
export function matrixIgnore(kind: Kind): string[] {
  if (isFull()) return [];
  return specFiles().filter((f) => !headerOf(f)?.tags.includes(kind)).map((f) => `**/${f}`);
}

/** Config-level title filters for the quarantine (project-level grep, if any, is applied on top). */
export function quarantineFilter(): { grep?: RegExp; grepInvert?: RegExp } {
  const q = process.env.MM_E2E_QUARANTINE;
  if (q === "only") return { grep: /@quarantine/ };
  if (q === "1") return {};
  return { grepInvert: /@quarantine/ };
}
