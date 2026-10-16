// After both builds: writes the precache list and a version hash into dist/sw.js (replaces the "__MM_PRECACHE__" token).
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

export interface ManifestEntry {
  file: string;
  css?: string[];
  assets?: string[];
}
export interface Precache {
  version: string;
  urls: string[];
}

export function buildPrecache(manifest: Record<string, ManifestEntry>): Precache {
  const files = new Set<string>();
  for (const e of Object.values(manifest)) {
    files.add(e.file);
    for (const f of [...(e.css ?? []), ...(e.assets ?? [])]) files.add(f);
  }
  const urls = [...[...files].map((f) => "/" + f.replace(/^\/+/, "")), "/index.html"].sort();
  const version = createHash("sha256").update(urls.join("\n")).digest("hex").slice(0, 12);
  return { version, urls };
}

export function injectPrecache(source: string, p: Precache): string {
  const re = /(["'`])__MM_PRECACHE__\1/;
  if (!re.test(source)) throw new Error("sw.js has no __MM_PRECACHE__ token");
  return source.replace(re, () => JSON.stringify(p));
}

if (import.meta.main) {
  const dist = resolve(import.meta.dirname, "../dist");
  const manifest = JSON.parse(readFileSync(resolve(dist, ".vite/manifest.json"), "utf8")) as Record<string, ManifestEntry>;
  const out = resolve(dist, "sw.js");
  writeFileSync(out, injectPrecache(readFileSync(out, "utf8"), buildPrecache(manifest)));
}
