import { beforeEach, describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import SubNav from "./SubNav.svelte";
import { me } from "../../me.svelte";

const html = () => render(SubNav as never, { props: { tab: "library", routeName: "home" } } as never).body;

describe("SubNav shows whenever at least one link is visible (Revision 7/11)", () => {
  beforeEach(() => {
    me.loaded = true;
  });
  test("no visible link: nothing rendered", () => {
    me.allowed = [];
    expect(html()).not.toContain('data-testid="subnav"');
  });
  test("a single visible link still renders the row", () => {
    me.allowed = ["search_tracks"];
    const h = html();
    expect(h).toContain('data-testid="subnav"');
    expect(h).toContain('href="/library"');
    expect(h).not.toContain('href="/sounds"');
  });
  test("the Owner (every op) sees every Library link", () => {
    me.allowed = ["search_tracks", "search_sounds", "get_song_atlas", "list_records", "list_crates", "list_playlists"];
    const h = html();
    for (const p of ["/library", "/sounds", "/atlas", "/records", "/crates", "/playlists"]) expect(h).toContain(`href="${p}"`);
  });
});
