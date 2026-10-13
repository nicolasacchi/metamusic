import { defineConfig, devices } from "@playwright/test";
import { BASE_URL, HEADERS } from "./e2e/env";

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
const mobileIgnore = [...ignoreM2a(["judge", "comments", "human", "listen"]), ...ignoreM2b(["duels"])];
const firefoxIgnore = [...ignoreM2a(["comments", "listen"]), ...ignoreM2b(["ears", "compare"])];

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.spec.ts",
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: BASE_URL,
    extraHTTPHeaders: HEADERS,
    trace: "retain-on-failure",
  },
  projects: [
    { name: "chrome", use: { ...devices["Desktop Chrome"], channel: "chrome", launchOptions: chromeLaunch } },
    { name: "chrome-pixel7", use: { ...devices["Pixel 7"], channel: "chrome", launchOptions: chromeLaunch }, testIgnore: mobileIgnore },
    // WebKit-sized viewport/touch emulation on Chrome; real iOS Safari is covered by the device checklist.
    { name: "chrome-iphone15", use: { ...devices["iPhone 15"], channel: "chrome", defaultBrowserType: "chromium", launchOptions: chromeLaunch }, testIgnore: mobileIgnore },
    { name: "firefox", use: { ...devices["Desktop Firefox"], launchOptions: { firefoxUserPrefs: firefoxPrefs } }, testIgnore: firefoxIgnore },
  ],
});
