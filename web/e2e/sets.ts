// CI splits the Playwright projects over three jobs (MM_E2E_SET=a|b|c, one server and library each). Playwright's own
// --shard cannot do it: every project depends on the earlier phases, so the only top-level project is the last one and
// the shard would hold nearly everything. Each project is in exactly one set (web/src/lib/e2e-sets.test.ts); inside a
// set the phase ordering of playwright.config.ts is kept (dependencies on projects of another set are dropped: the
// other set runs on its own server, so its specs cannot disturb the count-based or per-track specs of this one).
// Balanced by test count and weight: m3-* are few but slow.
export const E2E_SETS: Record<string, string[]> = {
  a: ["chrome", "listen-chrome", "firefox-android", "firefox-android-strict", "m4a-offline", "m4a-track-chrome", "m4a-track-pixel7"],
  b: ["chrome-pixel7", "chrome-iphone15", "listen-chrome-pixel7", "listen-chrome-iphone15"],
  c: ["firefox", "listen-firefox", "m3-chrome", "m3-pixel7", "m3-iphone15", "m3-spend"],
};
