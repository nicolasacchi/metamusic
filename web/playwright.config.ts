import { defineConfig, devices, type PlaywrightTestConfig } from "@playwright/test";
import { HEADERS, OUTPUT_DIR } from "./e2e/env";
import { matrixIgnore, quarantineFilter } from "./e2e/matrix";

// Chrome must autoplay without a prior gesture so specs can start playback programmatically
// (the probe specs still use a real click, which exercises the gesture rule).
const chromeLaunch = { args: ["--autoplay-policy=no-user-gesture-required"] };
const firefoxPrefs = { "media.autoplay.default": 0, "media.autoplay.blocking_policy": 0 };

// Which spec runs on which device is decided by the `// matrix:` header of each spec file (e2e/matrix.ts).
// Hermetic e2e: every Playwright worker serves its own seeded metamusic (web/e2e/fixtures.ts), so no project or spec
// shares budgets, counters, fake GPU workers, listen counts or the library with another and there are no phases or
// project `dependencies` (any project may run beside any other), and CI shards the whole test list (--shard=i/N).
// The listen spec still has one track per project (ownTracks) and the M3 specs add tracks and spend to their own server.
const LISTEN = "**/m2a-listen.spec.ts";
// M3 (C86.5): the generation specs add tracks, jobs and spend rows to their worker's own library (never the shared one
// the count-based specs read). The desktop project runs every M3 spec except m3-spend; m3-variant and m3-jobs also run on
// the phones when tagged @mobile; m3-spend changes the caps and the arming and has its own project.
const M3 = ["variant", "batch", "compose", "jobs", "approval", "lineage", "songwriting", "spend"];
const ignoreM3 = (keep: string[]) => M3.filter((n) => !keep.includes(n)).map((n) => `**/m3-${n}.spec.ts`);
const M3_SPECS = "**/m3-*.spec.ts";
const AUTOADV = "**/m3-autoadvance.spec.ts";
const noM3 = ignoreM3([]); // the phase-less projects never run the M3 specs (auto-advance is not in the M3 list)
const noSpend = ignoreM3(["variant", "batch", "compose", "jobs", "approval", "lineage", "songwriting"]); // m3-spend has its own project
// M3 auto-advance: the refused-play, watchdog and queue specs run on desktop Chrome, the Pixel 7 and two Android-Firefox
// profiles (Firefox user agent, phone viewport, touch); the other projects add nothing.
// Firefox (Android) blocks audio autoplay by default (media.autoplay.default=1); policy 0 lets a play() after the
// first activation through, policy 2 only accepts a play() made synchronously inside the input handler (every
// play() after an await, and the one in `ended`, is refused). Real devices ran sticky activation (the telemetry
// shows auto-advance working at times); the strict project proves the player degrades to "tap to continue".
const androidFirefox = (policy: number) => ({
  ...devices["Desktop Firefox"],
  userAgent: "Mozilla/5.0 (Android 14; Mobile; rv:131.0) Gecko/131.0 Firefox/131.0",
  viewport: { width: 412, height: 915 },
  hasTouch: true,
  launchOptions: { firefoxUserPrefs: { "media.autoplay.default": 1, "media.autoplay.blocking_policy": policy } },
});

// Skipped tests in a full run (26 at M2b; M2c and M3 add none: m2c-reference is excluded from the firefox and
// iphone15 projects by config, so it never appears as a skip), all intentional project scoping decided at run time with
// test.skip(<project predicate>, reason). None is a missing capability: the e2e server runs with the pinned
// ffmpeg/metaflac from bin/ on PATH (scripts/e2e.sh, CI "media tools on PATH"), so Tier A analysis and
// clips are always available, and nothing may skip on a missing tool.
//   m2a-comments   x3  firefox          runs only the "jump:" tests (C41 budget)
//   m2a-human      x4  pixel7, iphone15 the two pairing tests run on desktop chrome only (pairing codes are global)
//   m2b-duels      x19 each mobile project skips 9 (8 keyboard/desktop-only tests and the serial queue/skip test,
//                      which is chrome only) = 18; desktop chrome skips the mobile-toggle test = 1
// A new skip needs a reason of this kind; anything capability- or environment-related must fail instead.
// CI runs one `--shard=i/N` slice of the whole test list per job; no flag runs everything (make e2e).

const allProjects: NonNullable<PlaywrightTestConfig["projects"]> = [
    { name: "chrome", use: { ...devices["Desktop Chrome"], channel: "chrome", launchOptions: chromeLaunch }, testIgnore: [LISTEN, ...ignoreM3([])] },
    { name: "chrome-pixel7", use: { ...devices["Pixel 7"], channel: "chrome", launchOptions: chromeLaunch }, testIgnore: [...noM3, LISTEN, ...matrixIgnore("mobile")] },
    { name: "firefox-android", use: androidFirefox(0), testIgnore: [...noM3, LISTEN, ...matrixIgnore("android-firefox")] },
    { name: "firefox-android-strict", use: androidFirefox(2), testIgnore: [...noM3, LISTEN, ...matrixIgnore("android-firefox")], grep: /strict autoplay/ },
    // WebKit-sized viewport/touch emulation on Chrome; real iOS Safari is covered by the device checklist.
    { name: "chrome-iphone15", use: { ...devices["iPhone 15"], channel: "chrome", defaultBrowserType: "chromium", launchOptions: chromeLaunch }, testIgnore: [...noM3, LISTEN, ...matrixIgnore("mobile")] },
    { name: "firefox", use: { ...devices["Desktop Firefox"], launchOptions: { firefoxUserPrefs: firefoxPrefs } }, testIgnore: [...noM3, LISTEN, ...matrixIgnore("firefox")] },
    // the listen spec has one project per device (each owns a seed track)
    { name: "listen-chrome", use: { ...devices["Desktop Chrome"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: LISTEN },
    { name: "listen-chrome-pixel7", use: { ...devices["Pixel 7"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: LISTEN, testIgnore: matrixIgnore("mobile") },
    { name: "listen-chrome-iphone15", use: { ...devices["iPhone 15"], channel: "chrome", defaultBrowserType: "chromium", launchOptions: chromeLaunch }, testMatch: LISTEN, testIgnore: matrixIgnore("mobile") },
    { name: "listen-firefox", use: { ...devices["Desktop Firefox"], launchOptions: { firefoxUserPrefs: firefoxPrefs } }, testMatch: LISTEN, testIgnore: matrixIgnore("firefox") },
    // the M3 specs (see M3 above)
    { name: "m3-chrome", use: { ...devices["Desktop Chrome"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: M3_SPECS, testIgnore: [...noSpend, AUTOADV] },
    { name: "m3-pixel7", use: { ...devices["Pixel 7"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: M3_SPECS, testIgnore: [...noSpend, AUTOADV, ...matrixIgnore("mobile")] },
    { name: "m3-iphone15", use: { ...devices["iPhone 15"], channel: "chrome", defaultBrowserType: "chromium", launchOptions: chromeLaunch }, testMatch: M3_SPECS, testIgnore: [...noSpend, AUTOADV, ...matrixIgnore("mobile")] },
    { name: "m3-spend", use: { ...devices["Desktop Chrome"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: M3_SPECS, testIgnore: [...ignoreM3(["spend"]), AUTOADV] },
];

export default defineConfig({
  testDir: "./e2e",
  outputDir: OUTPUT_DIR,
  testMatch: "**/*.spec.ts",
  ...quarantineFilter(), // @quarantine tests: skipped unless MM_E2E_QUARANTINE=1|only (e2e/matrix.ts)
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: true,
  // fewer workers than cores: the specs assert on real-time audio/layout and flake when starved
  workers: process.env.CI ? 2 : "50%",
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    extraHTTPHeaders: HEADERS,
    trace: "retain-on-failure",
    // M4a (C117.6): service workers are blocked by default so the route mocks and request observations of the other specs
    // stay intact; m4a-offline and m4a-track opt in with test.use({ serviceWorkers: "allow" }) (their server is per worker).
    serviceWorkers: "block",
  },
  projects: allProjects,
});
