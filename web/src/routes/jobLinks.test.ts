import { expect, test } from "bun:test";
import { APP_ROUTES, matchRoute } from "../lib/router";

const src = await Bun.file(new URL("./Job.svelte", import.meta.url)).text();

test("Job page links to /tracks/:id (a real SPA route), never /track/", () => {
  const hrefs = [...src.matchAll(/href="(\/[a-z]+\/)\{/g)].map((m) => m[1] ?? "");
  expect(hrefs).toContain("/tracks/");
  expect(hrefs).not.toContain("/track/");
  for (const h of hrefs) expect(matchRoute(APP_ROUTES, h + "abc")).not.toBeNull();
});
