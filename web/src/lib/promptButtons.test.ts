import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import { createSession } from "../api/session";
import { copyPrompt, promptText } from "./promptButtons";

function rig(handler: (u: URL) => Response) {
  const reqs: URL[] = [];
  const api = createApi({
    baseUrl: "http://localhost",
    session: createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} }, sleep: async () => {}, onLine: () => true, fetch: async () => new Response("{}") }),
    fetch: async (req) => {
      const u = new URL(req.url);
      reqs.push(u);
      return handler(u);
    },
  });
  return { api, reqs };
}
const view = (text: string) => ({
  name: "critique_track",
  title: "t",
  description: "d",
  version: "1",
  arguments: [],
  messages: [
    { role: "user", content: { type: "text", text } },
    { role: "user", content: { type: "resource", resource: { uri: "mm://x", mime_type: "text/markdown", text: "CTX" } } },
  ],
});
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json" } });

describe("promptText", () => {
  test("joins text and embedded resource messages", () => {
    expect(promptText(view("you cannot hear audio") as never)).toBe("you cannot hear audio\n\nCTX");
  });
});

describe("copyPrompt", () => {
  test("one get_prompt call per click, track id passed, chars reported and clipboard written", async () => {
    const { api, reqs } = rig(() => json(view("hello")));
    const written: string[] = [];
    const r = await copyPrompt(api, "critique_track", "trk1", { writeText: async (t) => void written.push(t) });
    expect(reqs).toHaveLength(1);
    expect(reqs[0]!.pathname).toBe("/api/prompts/critique_track");
    expect(reqs[0]!.searchParams.get("track_id")).toBe("trk1");
    expect(r).toEqual({ ok: true, copied: true, chars: "hello\n\nCTX".length });
    expect(written).toEqual(["hello\n\nCTX"]);
  });

  test("a refused clipboard still reports the length; an API error reports its message", async () => {
    const a = rig(() => json(view("hi")));
    const r = await copyPrompt(a.api, "write_variant", "t", { writeText: async () => { throw new Error("denied"); } });
    expect(r).toEqual({ ok: true, copied: false, chars: 7 });
    const b = rig(() => json({ code: "RECIPE_UNKNOWN", message: "recipe unknown", details: {} }, 422));
    expect(await copyPrompt(b.api, "write_variant", "t", undefined)).toEqual({ ok: false, message: "recipe unknown" });
  });
});
