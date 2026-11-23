import { spawnSync } from "node:child_process";
import { join, resolve } from "node:path";

/**
 * Configures the e2e servers' soundfont (T1b): when fluidsynth is installed and the operator has not set MM_SOUNDFONTS, writes the
 * generated test SF2 (tools/sf2gen, 128 programs; nothing is committed) and points MM_SOUNDFONTS at it. Workers inherit the
 * environment of the global setup, so every server of the run lists the score_render kind and can make Desk auditions. Without
 * fluidsynth nothing is set and the specs assert the "not available" answers instead (CI sets MM_REQUIRE_FLUIDSYNTH=1, which makes
 * them demand the real path).
 */
export function configureTestSoundfont(dir: string): void {
  if (process.env.MM_SOUNDFONTS) return;
  const probe = spawnSync("fluidsynth", ["--version"], { stdio: "ignore" });
  if (probe.error || probe.status !== 0) return;
  const out = join(dir, "test.sf2");
  const root = resolve(import.meta.dirname, "../..");
  const gen = spawnSync("go", ["run", "./tools/sf2gen", "-out", out, "-presets", "128"], { cwd: root, stdio: ["ignore", "ignore", "inherit"] });
  if (gen.status !== 0) {
    if (process.env.MM_REQUIRE_FLUIDSYNTH === "1") throw new Error("e2e: could not generate the test soundfont (go run ./tools/sf2gen failed)");
    return;
  }
  process.env.MM_SOUNDFONTS = `gm=${out}`;
}
