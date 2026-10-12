import { defineConfig, devices } from "@playwright/test";
import { BASE_URL, HEADERS } from "./e2e/env";

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.spec.ts",
  globalSetup: "./e2e/global-setup.ts",
  globalTeardown: "./e2e/global-teardown.ts",
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
    { name: "chrome", use: { ...devices["Desktop Chrome"], channel: "chrome" } },
    { name: "chrome-pixel7", use: { ...devices["Pixel 7"], channel: "chrome" } },
    // WebKit-sized viewport/touch emulation on Chrome; real iOS Safari is covered by the device checklist.
    { name: "chrome-iphone15", use: { ...devices["iPhone 15"], channel: "chrome", defaultBrowserType: "chromium" } },
    { name: "firefox", use: { ...devices["Desktop Firefox"] } },
  ],
});
