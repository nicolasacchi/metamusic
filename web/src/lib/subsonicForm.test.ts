import { describe, expect, test } from "bun:test";
import { buildClientBody, canSubmit, emptyClientForm, slugUsername } from "./subsonicForm";

describe("subsonic client form", () => {
  test("username is derived from the name until edited", () => {
    expect(slugUsername("Pixel 7 — Symfonium!")).toBe("pixel-7-symfonium");
    expect(slugUsername("  --  ")).toBe("");
    const f = { ...emptyClientForm(), name: "My Phone" };
    expect(buildClientBody(f)).toMatchObject({ name: "My Phone", username: "my-phone" });
    expect(buildClientBody({ ...f, username: "custom", usernameEdited: true })).toMatchObject({ username: "custom" });
  });
  test("API key and Password are independent toggles, both default on", () => {
    const f = { ...emptyClientForm(), name: "x" };
    expect(f.apiKey && f.password).toBe(true);
    expect(buildClientBody({ ...f, password: false })).toMatchObject({ api_key: true, password: false });
    expect(buildClientBody({ ...f, apiKey: false })).toMatchObject({ api_key: false, password: true });
  });
  test("both toggles off disables submit", () => {
    const f = { ...emptyClientForm(), name: "x" };
    expect(canSubmit(f)).toBe(true);
    expect(canSubmit({ ...f, apiKey: false, password: false })).toBe(false);
  });
  test("a blank name disables submit; a name with no slug needs a username", () => {
    expect(canSubmit(emptyClientForm())).toBe(false);
    expect(canSubmit({ ...emptyClientForm(), name: "!!!" })).toBe(false);
    expect(canSubmit({ ...emptyClientForm(), name: "!!!", username: "ok", usernameEdited: true })).toBe(true);
  });
  test("write and default format pass through", () => {
    expect(buildClientBody({ ...emptyClientForm(), name: "x", write: false, defaultFormat: "mp3" })).toMatchObject({ write: false, default_format: "mp3" });
  });
});
