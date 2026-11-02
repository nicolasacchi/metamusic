<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { routeReady } from "../lib/scrollRestore";
  import { viewport } from "../lib/viewport.svelte";
  import { fetchViewer } from "../lib/viewer";
  import type { Viewer } from "../lib/genIntent";
  import { listCrates } from "../lib/crateApi";
  import { atlasGetter, axisPairError, boardHref, loadAtlas, applySelect, readyOnce, sharedMuse, totalsText, visiblePoints, type AtlasPage } from "../lib/atlasApi";
  import { filterFromSearch, filterToQuery, filterToSearch, MAX_VIEW, type AtlasFilter } from "../lib/atlasView";
  import AtlasCanvas from "../lib/components/AtlasCanvas.svelte";
  import AtlasTable from "../lib/components/AtlasTable.svelte";
  import AtlasToolbar from "../lib/components/AtlasToolbar.svelte";
  import AtlasLegend from "../lib/components/AtlasLegend.svelte";
  import AtlasSelection from "../lib/components/AtlasSelection.svelte";
  import AdherencePanel from "../lib/components/AdherencePanel.svelte";

  type Session = components["schemas"]["SessionSummary"];
  type Facets = components["schemas"]["SoundSearch"]["facets"];

  interface Props {
    /** window.location.search; the shell may omit it, the page then reads the address bar itself. */
    search?: string;
    navigate?: (to: string, replace?: boolean) => void;
    /** Test seams: a loaded map, the open tab, and the table view, without fetching. */
    initial?: AtlasPage;
    initialTab?: "map" | "adherence";
    initialTable?: boolean;
    viewer?: Viewer | null;
  }
  let { search, navigate, initial, initialTab = "map", initialTable = false, viewer = null }: Props = $props();

  const startSearch = () => search ?? (typeof window === "undefined" ? "" : window.location.search);
  let filter = $state<AtlasFilter>(filterFromSearch(startSearch()));
  // svelte-ignore state_referenced_locally
  let atlas = $state<AtlasPage | null>(initial ?? null);
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);
  let loaded = $state(0);
  let capped = $state(false);
  let pendingLeft = $state(false);
  let error = $state("");
  let stale = $state(false);
  // svelte-ignore state_referenced_locally
  let tab = $state<"map" | "adherence">(initialTab);
  // svelte-ignore state_referenced_locally
  let asTable = $state(initialTable);
  // D-L3-14: predicted points are estimates, hidden until the box is unticked.
  let hidePredicted = $state(true);
  let selectMode = $state(false);
  let selected = $state<ReadonlySet<string>>(new Set());
  let focusColor = $state<number | null>(null);
  let sessions = $state<Session[]>([]);
  let facets = $state<Facets>({});
  let crates = $state<{ id: string; name: string }[]>([]);
  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(viewer);
  let seq = 0;
  const ready = readyOnce(() => void routeReady());

  const axisError = $derived(axisPairError(filter.x, filter.y));
  const points = $derived(atlas ? visiblePoints(atlas.points, hidePredicted) : []);
  const ids = $derived([...selected]);
  const muse = $derived(atlas ? sharedMuse(atlas.points, atlas.muses, selected) : "");
  const nearTitle = $derived(filter.near ? (atlas?.points.find((p) => p.id === filter.near)?.t ?? "") : "");

  function go(f: AtlasFilter) {
    const s = filterToSearch(f);
    const to = "/atlas" + (s ? "?" + s : "");
    if (navigate) navigate(to, false);
    else if (typeof history !== "undefined") history.replaceState(history.state, "", to);
  }
  function commit(next: AtlasFilter) {
    filter = next;
    selected = new Set();
    focusColor = null;
    go(next);
  }

  async function load(f: AtlasFilter) {
    const mine = ++seq;
    error = "";
    stale = false;
    if (axisPairError(f.x, f.y)) {
      loading = false;
      ready();
      return;
    }
    loading = true;
    loaded = 0;
    try {
      const r = await loadAtlas(atlasGetter(api), filterToQuery(f), {
        cancelled: () => mine !== seq,
        onProgress: (p, n) => {
          if (mine !== seq) return;
          atlas = p;
          loaded = n;
          ready();
        },
      });
      if (mine !== seq || r.kind === "cancelled") return;
      if (r.kind === "ok") {
        atlas = r.atlas;
        capped = r.capped;
        pendingLeft = r.pendingLeft;
      } else if (r.kind === "stale") stale = true;
      else error = r.failure.message;
    } catch {
      if (mine === seq) error = "Could not load the Atlas.";
    } finally {
      if (mine === seq) {
        loading = false;
        ready();
      }
    }
  }

  async function loadAside() {
    try {
      const [s, f, c] = await Promise.all([
        api.GET("/api/sessions", { params: { query: { limit: 100 } } }),
        api.GET("/api/sounds/search", { params: { query: { limit: 0 } } }),
        listCrates(api, { subject: "song", limit: 100 }),
      ]);
      if (s.data) sessions = s.data.items;
      if (f.data) facets = f.data.facets;
      if (c.ok) crates = c.value.items.filter((x) => !x.hidden).map((x) => ({ id: x.id, name: x.name }));
    } catch {
      /* the filters degrade to empty lists */
    }
  }

  $effect(() => {
    const f = filter;
    if (initial) return;
    void load(f);
  });
  $effect(() => {
    if (initial) return;
    void loadAside();
  });
  $effect(() => {
    if (viewer) return;
    void fetchViewer().then((v) => (me = v));
  });
  // The shell hands the address bar's search down: adopt a change that did not come from this page.
  $effect(() => {
    if (search === undefined) return;
    const next = filterFromSearch(search);
    if (filterToSearch(next) !== untrack(() => filterToSearch(filter))) {
      filter = next;
      selected = new Set();
    }
  });
  onMount(() => {
    if (initial) ready();
    const onPop = () => {
      const next = filterFromSearch(window.location.search);
      if (filterToSearch(next) !== filterToSearch(filter)) {
        filter = next;
        selected = new Set();
      }
    };
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  });

  function onselect(pick: string[], mode: "toggle" | "add" | "clear") {
    selected = applySelect(selected, pick, mode);
  }
  const open = (id: string) => (navigate ? navigate(`/songs/${encodeURIComponent(id)}`) : window.location.assign(`/songs/${encodeURIComponent(id)}`));
  const go2 = (to: string) => (navigate ? navigate(to) : window.location.assign(to));
</script>

<section data-testid="atlas-page">
  <h1>Atlas</h1>

  <div class="tabs" role="tablist" aria-label="Atlas views">
    <button type="button" role="tab" aria-selected={tab === "map"} onclick={() => (tab = "map")} data-testid="atlas-tab-map">Map</button>
    <button type="button" role="tab" aria-selected={tab === "adherence"} onclick={() => (tab = "adherence")} data-testid="atlas-tab-adherence">Requested vs got</button>
  </div>

  {#if tab === "adherence"}
    <AdherencePanel />
  {:else}
    <AtlasToolbar {filter} {sessions} {facets} {crates} {axisError} {hidePredicted} {selectMode} touch={viewport.touch} onchange={commit} onhidepredicted={(h) => (hidePredicted = h)} onselectmode={(on) => (selectMode = on)} />

    {#if error}<p role="alert" data-testid="atlas-error">{error}</p>{/if}
    {#if stale}
      <p role="alert" data-testid="atlas-stale">Map changed. <button type="button" onclick={() => void load(filter)}>Reload</button></p>
    {/if}

    {#if atlas}
      <p class="muted totals" data-testid="atlas-totals" aria-busy={loading}>{totalsText(atlas.totals)}</p>
      {#if loading}<p class="muted" role="status" data-testid="atlas-progress">Loading… {loaded} points so far</p>{/if}
      {#if capped}<p class="muted" data-testid="atlas-capped">Showing the first {MAX_VIEW} points. Narrow the filters to see the rest.</p>{/if}
      {#if pendingLeft && !loading}<p class="muted" data-testid="atlas-pending">The layout is still settling; reload in a moment.</p>{/if}
      {#if filter.near && atlas.neighbors && atlas.neighbors.length > 0}
        <section class="near" data-testid="atlas-neighbours" aria-label="Nearest Songs">
          <h2>Nearest to {nearTitle || "this Song"}</h2>
          <ol>
            {#each atlas.neighbors as n (n.id)}<li><a href="/songs/{encodeURIComponent(n.id)}">{n.title}</a></li>{/each}
          </ol>
        </section>
      {/if}

      <div class="view">
        <button type="button" aria-pressed={asTable} onclick={() => (asTable = !asTable)} data-testid="atlas-table-toggle">{asTable ? "Show as map" : "Show as table"}</button>
      </div>

      {#if asTable}
        <AtlasTable {points} axes={atlas.axes} legend={atlas.legend} {selected} ontoggle={(id) => onselect([id], "toggle")} onopen={open} />
      {:else}
        <AtlasCanvas {points} axes={atlas.axes} guides={atlas.guides} {hidePredicted} {focusColor} {selected} {selectMode} onopen={open} {onselect} />
      {/if}
      <AtlasLegend legend={atlas.legend} colorBy={atlas.color_by} focus={focusColor} onfocus={(c) => (focusColor = c)} />
    {:else if loading}
      <p class="muted" data-testid="atlas-loading">Loading…</p>
    {:else if !error && !stale && !axisError}
      <p class="muted">Nothing to show.</p>
    {/if}

    <AtlasSelection {ids} boardHref={boardHref(filter, muse)} viewer={me} sheet={viewport.phone} navigate={go2} onclear={() => onselect([], "clear")} />
    {#if viewport.phone && ids.length > 0}<div class="spacer" aria-hidden="true"></div>{/if}
  {/if}
</section>

<style>
  .tabs {
    display: flex;
    gap: var(--sp-1);
    margin-bottom: var(--sp-3);
  }
  .tabs button {
    min-height: var(--hit);
  }
  .tabs button[aria-selected="true"] {
    background: var(--accent-soft);
    outline: 1px solid var(--accent);
  }
  .view {
    margin: var(--sp-2) 0;
  }
  .view button {
    min-height: var(--hit);
  }
  .totals {
    font-size: var(--fs-sm);
  }
  .near h2 {
    margin: var(--sp-2) 0;
    font-size: var(--fs-md);
  }
  .near ol {
    margin: 0 0 var(--sp-2);
    padding-left: var(--sp-5);
  }
  .near a {
    display: inline-flex;
    align-items: center;
    min-height: var(--hit);
  }
  .spacer {
    height: 8rem;
  }
</style>
