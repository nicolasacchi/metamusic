import { describe, expect, test } from "bun:test";
import { mediaKey, route, type Mode } from "./routes";

const sha = "a".repeat(64);
type Case = [string, string, Mode, string];
const cases: Case[] = [
  // non-GET is never intercepted
  ["POST", `/media/${sha}.flac`, "cors", "pass"],
  ["PUT", "/assets/app.js", "cors", "pass"],
  ["HEAD", "/", "navigate", "pass"],
  // server-owned prefixes always pass
  ["GET", "/api/tracks", "cors", "pass"],
  ["GET", "/human/api/x", "cors", "pass"],
  ["GET", "/auth/login", "cors", "pass"],
  ["GET", "/mcp", "cors", "pass"],
  ["GET", "/mcp/x", "cors", "pass"],
  ["GET", "/rest/ping.view?u=x", "cors", "pass"],
  ["GET", "/worker/x", "cors", "pass"],
  ["GET", "/.well-known/x", "cors", "pass"],
  ["GET", "/__e2e/x", "cors", "pass"],
  ["GET", "/healthz", "cors", "pass"],
  ["GET", "/sw.js", "same-origin", "pass"],
  ["GET", "/sw.js", "navigate", "pass"],
  ["GET", "/api/tracks", "navigate", "pass"],
  ["GET", "/mcp", "navigate", "pass"],
  // media
  ["GET", `/media/${sha}.flac`, "no-cors", "media"],
  ["GET", `/media/${sha}.mp3?exp=1&sig=x`, "no-cors", "media"],
  ["GET", `/media/${sha}.png`, "no-cors", "media"],
  ["GET", `/media/${"A".repeat(64)}.flac`, "no-cors", "pass"],
  ["GET", `/media/${"a".repeat(63)}.flac`, "no-cors", "pass"],
  ["GET", `/media/${sha}.wav`, "no-cors", "pass"],
  ["GET", `/media/${sha}.flac/x`, "no-cors", "pass"],
  // shell
  ["GET", "/assets/index-abc.js", "cors", "shell"],
  ["GET", "/manifest.webmanifest", "cors", "shell"],
  ["GET", "/icons/icon-192.png", "no-cors", "shell"],
  // navigations
  ["GET", "/start", "navigate", "navigate-server"],
  ["GET", "/pair", "navigate", "navigate-server"],
  ["GET", "/pair/x", "navigate", "navigate-server"],
  ["GET", "/auth/oidc/callback", "navigate", "navigate-server"],
  ["GET", "/human/tokens", "navigate", "navigate-server"],
  ["GET", "/human/connectors", "navigate", "navigate-server"],
  ["GET", "/", "navigate", "navigate-spa"],
  ["GET", "/tracks/abc", "navigate", "navigate-spa"],
  ["GET", "/offline", "navigate", "navigate-spa"],
  ["GET", "/started", "navigate", "navigate-spa"],
  // non-navigation requests to unknown paths pass
  ["GET", "/tracks/abc", "cors", "pass"],
  ["GET", "/start", "cors", "pass"],
  ["GET", "/human/tokens", "cors", "pass"],
  ["GET", "/pair", "same-origin", "pass"],
];

describe("route", () => {
  for (const [method, url, mode, want] of cases) {
    test(`${method} ${url.replace(sha, "<sha>")} (${mode}) -> ${want}`, () => {
      expect(route(new URL(url, "https://mm.example.com"), method, mode)).toBe(want as ReturnType<typeof route>);
    });
  }
  test("a foreign origin always passes", () => {
    expect(route(new URL(`https://cdn.example.net/media/${sha}.flac`), "GET", "no-cors", "https://mm.example.com")).toBe("pass");
  });
});

describe("mediaKey", () => {
  test("parses sha and ext", () => {
    expect(mediaKey(`/media/${sha}.flac`)).toEqual({ sha, ext: "flac" });
    expect(mediaKey("/media/x.flac")).toBeNull();
  });
});
