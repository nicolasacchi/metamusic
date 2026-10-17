<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import FacetPanel from "../lib/components/FacetPanel.svelte";
  import SongChip from "../lib/components/SongChip.svelte";
  import TaxonomyTable from "../lib/components/TaxonomyTable.svelte";
  import { summaryToChip } from "../lib/songView";
  import {
    FACETS,
    MAPPABLE_FACETS,
    FACET_LABELS,
    orderedFacets,
    selectedPills,
    soundsFromSearch,
    soundsToQuery,
    soundsToSearch,
    toggleSound,
    visibleFacets,
    type MappableFacet,
    type SoundsState,
  } from "../lib/soundsView";
  import type { Viewer } from "../lib/genIntent";
  import { fetchViewer } from "../lib/viewer";

  type Search = components["schemas"]["SoundSearch"];
  type Taxonomy = components["schemas"]["TaxonomyPage"];
  type SongSummary = components["schemas"]["SongSummary"];

  interface Props {
    /** window.location.search */
    search: string;
    navigate: (to: string, replace?: boolean) => void;
    /** Test seams: pre-loaded payloads (no fetching). */
    initial?: Search;
    initialTaxonomy?: Taxonomy;
    viewer?: Viewer | null;
  }
  let { search, navigate, initial, initialTaxonomy, viewer = null }: Props = $props();

  const sel = $derived(soundsFromSearch(search));
  // svelte-ignore state_referenced_locally
  let result = $state<Search | null>(initial ?? null);
  // svelte-ignore state_referenced_locally
  let songs = $state<SongSummary[]>(initial?.songs ?? []);
  // svelte-ignore state_referenced_locally
  let taxonomy = $state<Taxonomy | null>(initialTaxonomy ?? null);
  let unmapped = $state<components["schemas"]["UnmappedTerm"][]>([]);
  let facetFilter = $state("");
  let q = $state("");
  let error = $state("");
  let note = $state("");
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);
  let loadingMore = $state(false);
  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(viewer);
  let seq = 0;

  const blocks = $derived(result ? orderedFacets(result.facets) : []);
  let showAll = $state(false);
  const vis = $derived(visibleFacets(blocks, sel.sounds, showAll));
  const pills = $derived(selectedPills(sel.sounds));
  const canMap = $derived(me !== null && me.kind !== "device" && me.scopes.includes("write"));

  function commit(next: SoundsState) {
    const s = soundsToSearch(next);
    navigate("/sounds" + (s ? "?" + s : ""), false);
  }

  // The generated client types do not know the shared song filters on search_sounds yet (see the lane notes), hence the cast.
  async function fetchSearch(query: object) {
    return api.GET("/api/sounds/search", { params: { query: query as never } });
  }

  async function loadSongs(s: SoundsState) {
    const mine = ++seq;
    loading = true;
    error = "";
    try {
      const r = await fetchSearch(soundsToQuery(s, { limit: 50 }));
      if (mine !== seq) return;
      if (!r.data) {
        error = r.error?.message ?? "Failed to load the Sounds";
        return;
      }
      result = r.data;
      songs = r.data.songs;
    } catch {
      if (mine === seq) error = "Failed to load the Sounds";
    } finally {
      if (mine === seq) loading = false;
    }
  }

  async function more() {
    if (!result?.next_cursor || loadingMore) return;
    loadingMore = true;
    try {
      const r = await fetchSearch(soundsToQuery(sel, { limit: 50, cursor: result.next_cursor }));
      if (r.data) {
        const have = new Set(songs.map((x) => x.id));
        songs = [...songs, ...r.data.songs.filter((x) => !have.has(x.id))];
        result = { ...r.data, songs };
      } else error = r.error?.message ?? "Failed to load more";
    } catch {
      error = "Failed to load more";
    } finally {
      loadingMore = false;
    }
  }

  async function loadTaxonomy() {
    try {
      const query = { ...(facetFilter ? { facet: facetFilter } : {}), ...(q.trim() ? { q: q.trim() } : {}), limit: 200 };
      const [t, u] = await Promise.all([
        api.GET("/api/sounds/taxonomy", { params: { query: query as never } }),
        api.GET("/api/sounds/taxonomy", { params: { query: { unmapped: true, limit: 20 } } }),
      ]);
      if (t.data) taxonomy = t.data;
      else error = t.error?.message ?? "Failed to load the taxonomy";
      if (u.data) unmapped = u.data.unmapped;
    } catch {
      error = "Failed to load the taxonomy";
    }
  }

  async function onmap(m: { term: string; facet: MappableFacet; value: string }): Promise<string> {
    try {
      const r = await api.POST("/api/sound-terms", { body: { mappings: [{ term: m.term, facet: m.facet, value: m.value, op: "add" }] } });
      if (!r.data) return r.error?.message ?? "The mapping was refused";
      const c = r.data.recomputed;
      note = r.data.version === null ? "Already mapped; nothing changed." : `Mapped "${m.term}" (version ${r.data.version}); recomputed ${c.songs} songs and ${c.takes} takes.`;
      await Promise.all([loadTaxonomy(), loadSongs(sel)]);
      return "";
    } catch {
      return "Network error; try again.";
    }
  }

  $effect(() => {
    const s = sel;
    if (initial) return;
    void loadSongs(s);
  });
  $effect(() => {
    if (sel.tab !== "terms" || initialTaxonomy) return;
    void facetFilter;
    void q;
    void loadTaxonomy();
  });
  $effect(() => {
    if (viewer) return;
    void fetchViewer().then((v) => (me = v));
  });
</script>

<h1>Sounds</h1>

<div class="tabs" role="tablist" aria-label="Sounds view">
  <button type="button" role="tab" aria-selected={sel.tab === "songs"} data-testid="tab-songs" onclick={() => commit({ ...sel, tab: "songs" })}>Songs</button>
  <button type="button" role="tab" aria-selected={sel.tab === "terms"} data-testid="tab-terms" onclick={() => commit({ ...sel, tab: "terms" })}>Terms</button>
</div>

{#if error}<p role="alert" data-testid="sounds-error">{error}</p>{/if}
{#if note}<p role="status" data-testid="sounds-note">{note}</p>{/if}

{#if sel.tab === "songs"}
  <section aria-label="Selected" class="selected">
    {#each pills as p (p.key)}
      <button type="button" class="pill" data-testid="selected-pill" onclick={() => commit({ ...sel, sounds: toggleSound(sel.sounds, p.facet, p.value) })} aria-label="Remove {p.label}">{p.label} ×</button>
    {/each}
    <label class="check">
      <input type="checkbox" checked={sel.tempoMismatch} onchange={(e) => commit({ ...sel, tempoMismatch: e.currentTarget.checked })} data-testid="tempo-mismatch-toggle" />
      Tempo mismatch only
    </label>
    {#if pills.length > 0 || sel.tempoMismatch}
      <button type="button" data-testid="clear-sounds" onclick={() => commit({ ...sel, sounds: [], tempoMismatch: false })}>Clear</button>
    {/if}
  </section>

  <div class="facets" data-testid="facets">
    {#each vis.shown as b (b.facet)}
      <FacetPanel block={b} selected={sel.sounds} ontoggle={(f, v) => commit({ ...sel, sounds: toggleSound(sel.sounds, f, v) })} />
    {/each}
    {#if vis.hidden > 0 || showAll}
      <button type="button" class="allfacets" aria-pressed={showAll} data-testid="show-all-facets" onclick={() => (showAll = !showAll)}>
        {showAll ? "Hide empty facets" : `Show all facets (${vis.hidden} empty)`}
      </button>
    {/if}
  </div>

  <section aria-label="Songs" aria-busy={loading}>
    <h2>Songs <span class="muted" data-testid="songs-total">{result ? result.total : ""}</span></h2>
    {#if !loading && songs.length === 0}
      <p class="muted">No songs match.</p>
    {/if}
    <ul class="songs">
      {#each songs as s (s.id)}
        <li data-testid="sound-song">
          <SongChip chip={summaryToChip(s)} href="/songs/{s.id}" />
        </li>
      {/each}
    </ul>
    {#if result?.next_cursor}
      <button type="button" data-testid="songs-more" disabled={loadingMore} onclick={more}>Load more</button>
    {/if}
  </section>
{:else}
  <div class="filters">
    <label>
      Facet
      <select bind:value={facetFilter} data-testid="taxonomy-facet">
        <option value="">all</option>
        {#each MAPPABLE_FACETS as f (f)}<option value={f}>{FACET_LABELS[f]}</option>{/each}
      </select>
    </label>
    <label>
      Term
      <input type="search" bind:value={q} data-testid="taxonomy-q" maxlength="60" />
    </label>
    <span class="muted">Computed facets ({FACETS.filter((f) => !(MAPPABLE_FACETS as readonly string[]).includes(f)).map((f) => FACET_LABELS[f]).join(", ")}) are not mappable.</span>
  </div>
  {#if taxonomy}
    <TaxonomyTable items={taxonomy.items} {unmapped} versions={taxonomy.versions} currentVersion={taxonomy.current_version} {canMap} {onmap} />
  {:else}
    <p class="muted">Loading…</p>
  {/if}
{/if}

<style>
  .tabs {
    display: flex;
    gap: 0.4rem;
    margin: 0.5rem 0;
  }
  .tabs button[aria-selected="true"] {
    border-color: #f2a65a;
    background: #3a322b;
  }
  .selected {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: center;
    margin: 0.5rem 0;
  }
  .pill {
    border-radius: 1rem;
    border: 1px solid #f2a65a;
    background: #3a322b;
    color: inherit;
    padding: 0.1rem 0.6rem;
    cursor: pointer;
  }
  .facets {
    display: grid;
    gap: 0.8rem;
    margin: 0.6rem 0 1rem;
  }
  .songs {
    list-style: none;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.8rem;
    align-items: end;
    margin: 0.5rem 0;
  }
  .filters label {
    display: grid;
    font-size: 0.85rem;
  }
</style>
