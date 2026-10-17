<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { player } from "../lib/player.svelte";
  import { parseFilters, toApiQuery, toSearch, type Filters } from "../lib/libraryQuery";
  import { fmt } from "../lib/time";
  import LibraryFilters from "../lib/components/LibraryFilters.svelte";
  import Pager from "../lib/components/Pager.svelte";
  import TrackTable from "../lib/components/TrackTable.svelte";
  import { composeIntent, mayGenerate, startGeneration, templateIntent, toggled, validateIntent, type VariantMode } from "../lib/genIntent";

  type Whoami = components["schemas"]["WhoamiOutBody"];
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
      const [m, s, fc] = await Promise.allSettled([
        api.GET("/api/me"),
        api.GET("/api/stats"),
        api.GET("/api/library/facets"),
      ]);
      if (m.status === "fulfilled" && m.value.data) me = m.value.data;
      if (s.status === "fulfilled" && s.value.data) stats = s.value.data;
      if (fc.status === "fulfilled" && fc.value.data) facets = fc.value.data;
      if (!me) headerError = "Failed to load";
    })();
  });

  // Selection mode (C84.2): known lossless mothers for a template edit (2..50), or a new song.
  let selecting = $state(false);
  let selected = $state<Set<string>>(new Set());
  let templateMode = $state<VariantMode>("style_edit");
  const canGen = $derived(me !== null && mayGenerate(me));
  const intent = $derived(templateIntent([...selected], templateMode));
  const selError = $derived(selected.size === 0 ? "" : (validateIntent(intent) ?? ""));
  function toGenerate(i: ReturnType<typeof templateIntent>) {
    startGeneration(sessionStorage, i, (to) => location.assign(to));
  }

  /**
   * Playing from the list queues the visible order (the earlier rows stay reachable with Previous) and the
   * player asks for the next page before the queue runs out, so a long list does not stop at the page end.
   */
  function play(id: string) {
    const t = tracks.find((x) => x.id === id);
    let cursor = nextCursor;
    const f = filters;
    player.play(
      id,
      tracks.map((x) => x.id),
      {
        ...(t ? { title: t.title } : {}),
        more: async () => {
          if (!cursor) return [];
          const r = await api.GET("/api/tracks", { params: { query: { ...toApiQuery(f), limit: PAGE, cursor } } });
          if (!r.data) throw new Error("page failed");
          cursor = r.data.next_cursor;
          return r.data.items.map((x) => x.id);
        },
      },
    );
  }
  function playNext(id: string) {
    player.playNext(id);
  }
</script>

<h1>Library</h1>

<div class="strip" aria-label="Status">
  {#if stats}
    <span data-testid="library-counts">
      {stats.tracks.total} tracks · {fmt(stats.tracks.duration_s_total)} total · {stats.backlog.drafts} drafts
    </span>
  {/if}
</div>
{#if headerError}<p role="alert">{headerError}</p>{/if}

<LibraryFilters {filters} {facets} onchange={commit} />

{#if canGen}
  <div class="gen" data-testid="library-generate">
    <button type="button" onclick={() => toGenerate(composeIntent())} data-testid="new-song">New song</button>
    <button type="button" aria-pressed={selecting} onclick={() => { selecting = !selecting; selected = new Set(); }} data-testid="select-mode">
      {selecting ? "Cancel selection" : "Select tracks"}
    </button>
    {#if selecting}
      <label>
        Template
        <select bind:value={templateMode} data-testid="template-mode">
          <option value="style_edit">Edit style/BPM</option>
          <option value="lyric_edit">Edit lyrics</option>
          <option value="same_seed_edit">Same seed edit</option>
          <option value="new_seed">New seed</option>
        </select>
      </label>
      <button type="button" disabled={selected.size < 2 || selError !== ""} onclick={() => toGenerate(intent)} data-testid="template-edit">Template edit… ({selected.size})</button>
      {#if selError}<span role="alert" data-testid="selection-error">{selError}</span>{/if}
      {#if selected.size === 0}<span class="muted">Reference and lossy tracks cannot be selected: recipe unknown.</span>{/if}
    {/if}
  </div>
{/if}

{#if error}<p role="alert">{error}</p>{/if}
<div aria-busy={loading}>
  <TrackTable {tracks} playingId={player.state.trackId} onplay={play} onplaynext={playNext} selected={selecting ? selected : null} onselect={(id, on) => (selected = toggled(selected, id, on))} />
</div>
<Pager {page} offset={starts[page] ?? 0} count={tracks.length} {total} busy={loading} hasNext={nextCursor !== null} onprev={() => go(-1)} onnext={() => go(1)} />

<style>
  .gen {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 0.75rem;
    align-items: center;
    margin: 0.5rem 0;
  }
  .strip {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem 1.25rem;
    font-size: 0.85rem;
    margin-bottom: 1rem;
    opacity: 0.85;
  }
</style>
