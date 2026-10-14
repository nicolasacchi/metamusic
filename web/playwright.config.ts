import { defineConfig, devices } from "@playwright/test";
import { BASE_URL, HEADERS, OUTPUT_DIR } from "./e2e/env";

// Chrome must autoplay without a prior gesture so specs can start playback programmatically
// (the probe specs still use a real click, which exercises the gesture rule).
const chromeLaunch = { args: ["--autoplay-policy=no-user-gesture-required"] };
const firefoxPrefs = { "media.autoplay.default": 0, "media.autoplay.blocking_policy": 0 };

// C41 time budget for the M2a specs: the mobile projects run only judge, comments, human and listen;
// Firefox only the jump (comments) and listen specs; every other M2a spec runs on desktop Chrome only.
const M2A = ["judge", "comments", "drafts", "playlists", "export", "backup", "audit", "human", "listen", "context"];
const ignoreM2a = (keep: string[]) => M2A.filter((n) => !keep.includes(n)).map((n) => `**/m2a-${n}.spec.ts`);

// M2b budget (rev 3): the duel specs (keyboard on desktop, toggle on mobile) skip Firefox, whose headless
// AudioContext is not relied on for scheduling assertions; the ears and compare specs are layout- and
// data-only, so they run on desktop Chrome and Firefox but not on the phones.
const M2B = ["duels", "ears", "compare"];
const ignoreM2b = (keep: string[]) => M2B.filter((n) => !keep.includes(n)).map((n) => `**/m2b-${n}.spec.ts`);
// M2c: the reference/lossy spec runs on desktop Chrome and the Pixel 7 only (Firefox and the iPhone-sized project
// add no coverage for badges, filters and the MP3 fallback; real iOS playback is in the device checklist).
const M2C = ["reference"];
const ignoreM2c = (keep: string[]) => M2C.filter((n) => !keep.includes(n)).map((n) => `**/m2c-${n}.spec.ts`);
// The listen spec counts plays per seed track, and other specs also play seed tracks (auto-advance plays
// "Syn song 02" and the track after it, the judge specs play their own track): any play of the listen track
// between the spec's "before" and "after" reads as a duplicate. It therefore runs alone, as a second phase
// ("listen-*" projects depend on every other project) with one track per project.
const LISTEN = "**/m2a-listen.spec.ts";
// M3 (C86.5): the generation specs add tracks, jobs and spend rows to the shared library, which the count-based specs
// (m1-library, m1-pwa, m1-import, the audit and export specs) must never see. Every M3 project is therefore a third
// phase that depends on phase 1 and on the listen projects. The desktop project runs every M3 spec except m3-spend;
// m3-variant and m3-jobs also run on the phones, one project after the other (they share two fake workers); m3-spend
// changes the caps and the arming for everyone, so it runs alone after every other project.
const M3 = ["variant", "batch", "compose", "jobs", "approval", "lineage", "spend"];
const ignoreM3 = (keep: string[]) => M3.filter((n) => !keep.includes(n)).map((n) => `**/m3-${n}.spec.ts`);
const M3_SPECS = "**/m3-*.spec.ts";
const mobileIgnore = [...ignoreM2a(["judge", "comments", "human", "listen"]), ...ignoreM2b(["duels"]), ...ignoreM3([])];
const iphoneIgnore = [...mobileIgnore, ...ignoreM2c([])];
const firefoxIgnore = [...ignoreM2a(["comments", "listen"]), ...ignoreM2b(["ears", "compare"]), ...ignoreM2c([]), ...ignoreM3([])];
const phase1 = ["chrome", "chrome-pixel7", "chrome-iphone15", "firefox"];
const phase2 = ["listen-chrome", "listen-chrome-pixel7", "listen-chrome-iphone15", "listen-firefox"];
const before3 = [...phase1, ...phase2];
const mobileM3 = ignoreM3(["variant", "jobs"]);

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
export default defineConfig({
  testDir: "./e2e",
  outputDir: OUTPUT_DIR,
  testMatch: "**/*.spec.ts",
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: true,
  // fewer workers than cores: the specs assert on real-time audio/layout and flake when starved
  workers: process.env.CI ? 2 : "50%",
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: BASE_URL,
    extraHTTPHeaders: HEADERS,
    trace: "retain-on-failure",
  },
  projects: [
    { name: "chrome", use: { ...devices["Desktop Chrome"], channel: "chrome", launchOptions: chromeLaunch }, testIgnore: [LISTEN, M3_SPECS] },
    { name: "chrome-pixel7", use: { ...devices["Pixel 7"], channel: "chrome", launchOptions: chromeLaunch }, testIgnore: [...mobileIgnore, LISTEN] },
    // WebKit-sized viewport/touch emulation on Chrome; real iOS Safari is covered by the device checklist.
    { name: "chrome-iphone15", use: { ...devices["iPhone 15"], channel: "chrome", defaultBrowserType: "chromium", launchOptions: chromeLaunch }, testIgnore: [...iphoneIgnore, LISTEN] },
    { name: "firefox", use: { ...devices["Desktop Firefox"], launchOptions: { firefoxUserPrefs: firefoxPrefs } }, testIgnore: [...firefoxIgnore, LISTEN] },
    // phase 2: the listen spec alone (see LISTEN above)
    { name: "listen-chrome", use: { ...devices["Desktop Chrome"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: LISTEN, dependencies: phase1 },
    { name: "listen-chrome-pixel7", use: { ...devices["Pixel 7"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: LISTEN, dependencies: phase1 },
    { name: "listen-chrome-iphone15", use: { ...devices["iPhone 15"], channel: "chrome", defaultBrowserType: "chromium", launchOptions: chromeLaunch }, testMatch: LISTEN, dependencies: phase1 },
    { name: "listen-firefox", use: { ...devices["Desktop Firefox"], launchOptions: { firefoxUserPrefs: firefoxPrefs } }, testMatch: LISTEN, dependencies: phase1 },
    // phase 3: the M3 specs (see M3 above)
    { name: "m3-chrome", use: { ...devices["Desktop Chrome"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: M3_SPECS, testIgnore: ignoreM3(["variant", "batch", "compose", "jobs", "approval", "lineage"]), dependencies: before3 },
    { name: "m3-pixel7", use: { ...devices["Pixel 7"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: M3_SPECS, testIgnore: mobileM3, dependencies: [...before3, "m3-chrome"] },
    { name: "m3-iphone15", use: { ...devices["iPhone 15"], channel: "chrome", defaultBrowserType: "chromium", launchOptions: chromeLaunch }, testMatch: M3_SPECS, testIgnore: mobileM3, dependencies: [...before3, "m3-pixel7"] },
    { name: "m3-spend", use: { ...devices["Desktop Chrome"], channel: "chrome", launchOptions: chromeLaunch }, testMatch: M3_SPECS, testIgnore: ignoreM3(["spend"]), dependencies: [...before3, "m3-chrome", "m3-pixel7", "m3-iphone15"] },
  ],
});
