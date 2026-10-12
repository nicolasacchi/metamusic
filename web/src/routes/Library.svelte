<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { player } from "../lib/player.svelte";
  import { parseFilters, toApiQuery, toSearch, type Filters } from "../lib/libraryQuery";
  import { fmt } from "../lib/time";
  import LibraryFilters from "../lib/components/LibraryFilters.svelte";
  import Pager from "../lib/components/Pager.svelte";
  import TrackTable from "../lib/components/TrackTable.svelte";

  type Whoami = components["schemas"]["WhoamiOutBody"];
  type Ping = components["schemas"]["PingOutBody"];
  type Stats = components["schemas"]["Stats"];
  type Facets = components["schemas"]["Facets"];
  type Track = components["schemas"]["TrackSummary"];

  const PAGE = 50;

  interface Props {
    /** window.location.search (repeatable params survive; the router's query map collapses them). */
    search: string;
    navigate: (to: string, replace?: boolean) => void;
  }
  let { search, navigate }: Props = $props();

  let me = $state<Whoami | null>(null);
  let ping = $state<Ping | null>(null);
  let stats = $state<Stats | null>(null);
  let facets = $state<Facets | null>(null);
  let headerError = $state("");

  let tracks = $state<Track[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state("");
  // cursors[i] is the cursor that fetches page i (page 0 has none).
  let cursors = $state<(string | undefined)[]>([undefined]);
  let page = $state(0);
  let nextCursor = $state<string | null>(null);
  // starts[i] is the absolute offset of the first item on page i.
  let starts = $state<number[]>([0]);
  let seq = 0;

  const filters = $derived(parseFilters(search));

  function commit(f: Filters) {
    navigate("/" + toSearch(f), false);
  }

  async function load(f: Filters, cursor: string | undefined) {
    const mine = ++seq;
    loading = true;
    error = "";
    try {
      const r = await api.GET("/api/tracks", { params: { query: { ...toApiQuery(f), limit: PAGE, ...(cursor ? { cursor } : {}) } } });
      if (mine !== seq) return;
      if (!r.data) {
        error = r.error?.message ?? "Failed to load tracks";
        return;
      }
      tracks = r.data.items;
      total = r.data.total;
      nextCursor = r.data.next_cursor;
    } catch {
      if (mine === seq) error = "Failed to load tracks";
    } finally {
      if (mine === seq) loading = false;
    }
  }

  // New filters (URL changed): back to page 0.
  $effect(() => {
    const f = filters;
    cursors = [undefined];
    starts = [0];
    page = 0;
    nextCursor = null;
    void load(f, undefined);
  });

  function go(delta: number) {
    const target = page + delta;
    if (target < 0) return;
    if (delta > 0) {
      if (!nextCursor) return;
      cursors[target] = nextCursor;
      starts[target] = (starts[page] ?? 0) + tracks.length;
    }
    page = target;
    void load(filters, cursors[target]);
  }

  $effect(() => {
    void (async () => {
      const [m, p, s, fc] = await Promise.allSettled([
        api.GET("/api/me"),
        api.GET("/api/ping"),
        api.GET("/api/stats"),
        api.GET("/api/library/facets"),
      ]);
      if (m.status === "fulfilled" && m.value.data) me = m.value.data;
      if (p.status === "fulfilled" && p.value.data) ping = p.value.data;
      if (s.status === "fulfilled" && s.value.data) stats = s.value.data;
      if (fc.status === "fulfilled" && fc.value.data) facets = fc.value.data;
      if (!me || !ping) headerError = "Failed to load";
    })();
  });

  function play(id: string) {
    const t = tracks.find((x) => x.id === id);
    player.play(
      id,
      tracks.map((x) => x.id),
      t ? { title: t.title } : {},
    );
  }
</script>

<h1>Library</h1>

<div class="strip" aria-label="Status">
  <span>Identity <b data-testid="identity">{me ? me.name : "…"}</b></span>
  <span>Access <b data-testid="via">{me ? `${me.kind} via ${me.via} (${me.scopes.join(", ")})` : "…"}</b></span>
  <span>Server <b data-testid="version">{ping ? `${ping.ok ? "ok" : "down"} ${ping.version}` : "…"}</b></span>
  {#if stats}
    <span data-testid="library-counts">
      {stats.tracks.total} tracks · {fmt(stats.tracks.duration_s_total)} total · {stats.backlog.drafts} drafts
    </span>
  {/if}
</div>
{#if headerError}<p role="alert">{headerError}</p>{/if}

<LibraryFilters {filters} {facets} onchange={commit} />

{#if error}<p role="alert">{error}</p>{/if}
<div aria-busy={loading}>
  <TrackTable {tracks} playingId={player.state.trackId} onplay={play} />
</div>
<Pager {page} offset={starts[page] ?? 0} count={tracks.length} {total} busy={loading} hasNext={nextCursor !== null} onprev={() => go(-1)} onnext={() => go(1)} />

<style>
  .strip {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem 1.25rem;
    font-size: 0.85rem;
    margin-bottom: 1rem;
    opacity: 0.85;
  }
</style>
