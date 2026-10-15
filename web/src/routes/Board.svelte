<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import BoardGrid from "../lib/components/BoardGrid.svelte";
  import BoardToolbar from "../lib/components/BoardToolbar.svelte";
  import MusePicker from "../lib/components/MusePicker.svelte";
  import SessionPanel from "../lib/components/SessionPanel.svelte";
  import {
    buildGrid,
    draftsForEstimate,
    emptySelection,
    filtersFromSearch,
    filtersToQuery,
    filtersToSearch,
    mergeBoards,
    rangeSelect,
    selectAllDrafts,
    toggleChip,
    type Board,
    type BoardFilters,
    type Chip,
    type GridRow,
    type Selection,
  } from "../lib/boardView";
  import { MAX_TAKES, draftsIntent, mayGenerate, startGeneration, validateIntent, type Viewer } from "../lib/genIntent";
  import { assignSongs } from "../lib/museApi";
  import { fetchViewer } from "../lib/viewer";

  type Session = components["schemas"]["SessionSummary"];
  type Facets = components["schemas"]["SoundSearch"]["facets"];

  interface Props {
    /** window.location.search */
    search: string;
    navigate: (to: string, replace?: boolean) => void;
    /** Test seam: a first page to show without fetching. */
    initial?: Board;
    viewer?: Viewer | null;
  }
  let { search, navigate, initial, viewer = null }: Props = $props();

  const filters = $derived(filtersFromSearch(search));
  // svelte-ignore state_referenced_locally
  let board = $state<Board | null>(initial ?? null);
  let sessions = $state<Session[]>([]);
  let facets = $state<Facets>({});
  let selection = $state<Selection>(emptySelection());
  let note = $state("");
  let error = $state("");
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);
  let loadingMore = $state(false);
  let assigning = $state(false);
  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(viewer);
  let seq = 0;

  const grid = $derived(board ? buildGrid(board) : { columns: [], rows: [] });
  const estimate = $derived(draftsForEstimate(grid, selection));
  const canEstimate = $derived(mayGenerate(me) && estimate.draftIds.length > 0);

  function commit(next: BoardFilters) {
    const s = filtersToSearch(next);
    navigate("/board" + (s ? "?" + s : ""), false);
  }

  async function load(f: BoardFilters) {
    const mine = ++seq;
    loading = true;
    error = "";
    try {
      const r = await api.GET("/api/board", { params: { query: filtersToQuery(f) } });
      if (mine !== seq) return;
      if (!r.data) {
        error = r.error?.message ?? "Failed to load the Board";
        return;
      }
      board = r.data;
      selection = emptySelection();
      note = "";
    } catch {
      if (mine === seq) error = "Failed to load the Board";
    } finally {
      if (mine === seq) loading = false;
    }
  }

  async function more(cursor: string | null, unassignedAfter?: string | null) {
    if (loadingMore || !board) return;
    loadingMore = true;
    try {
      const q = { ...filtersToQuery(filters), ...(cursor ? { cursor } : {}), ...(unassignedAfter ? { unassigned_after: unassignedAfter } : {}) };
      const r = await api.GET("/api/board", { params: { query: q } });
      if (r.data) board = mergeBoards(board, r.data);
      else error = r.error?.message ?? "Failed to load more";
    } catch {
      error = "Failed to load more";
    } finally {
      loadingMore = false;
    }
  }

  async function loadAside() {
    try {
      const [s, f] = await Promise.all([api.GET("/api/sessions", { params: { query: { limit: 100 } } }), api.GET("/api/sounds/search", { params: { query: { limit: 0 } } })]);
      if (s.data) sessions = s.data.items;
      if (f.data) facets = f.data.facets;
    } catch {
      /* the filters degrade to empty lists */
    }
  }

  $effect(() => {
    const f = filters;
    if (initial) return;
    void load(f);
  });
  $effect(() => {
    if (viewer) return;
    void fetchViewer().then((v) => (me = v));
  });
  $effect(() => {
    if (initial) return;
    void loadAside();
  });

  function onchip(row: GridRow, chip: Chip, e: MouseEvent) {
    selection = e.shiftKey ? rangeSelect(selection, row, chip.id) : toggleChip(selection, chip.id);
  }
  function selectDrafts() {
    const r = selectAllDrafts(grid, selection);
    selection = r.selection;
    note = r.note;
  }
  function startEstimate() {
    const intent = draftsIntent(estimate.draftIds);
    const bad = validateIntent(intent);
    if (bad) {
      note = bad;
      return;
    }
    startGeneration(sessionStorage, intent, (to) => location.assign(to));
  }
  async function assign(muse: { id: string; title: string }) {
    const r = await assignSongs(api, muse.id, [...selection.ids]);
    assigning = false;
    if (r.kind === "ok") {
      note = `Assigned ${r.value.assigned} ${r.value.assigned === 1 ? "song" : "songs"} to the Muse.`;
      await load(filters);
    } else {
      error = r.kind === "failed" ? r.message || "Could not assign the songs." : "Network error.";
    }
  }
  const openId = $derived(selection.ids.size === 1 ? [...selection.ids][0]! : "");
</script>

<h1>Muse Board</h1>
{#if error}<p role="alert" data-testid="board-error">{error}</p>{/if}

<BoardToolbar {filters} {sessions} {facets} onchange={commit} />

<div class="actions" role="toolbar" aria-label="Selection actions">
  <span data-testid="selection-count">{selection.ids.size} selected</span>
  <button type="button" onclick={selectDrafts} data-testid="select-drafts">Select all drafts in view</button>
  <button type="button" disabled={!canEstimate} onclick={startEstimate} data-testid="estimate">Estimate &amp; render{#if estimate.draftIds.length > 0} ({estimate.draftIds.length}){/if}</button>
  <button type="button" disabled={selection.ids.size === 0} onclick={() => (assigning = !assigning)} data-testid="assign-open">Assign to Muse…</button>
  {#if openId}<a href="/songs/{openId}" data-testid="open-song">Open</a>{/if}
  <button type="button" disabled={selection.ids.size === 0} onclick={() => (selection = emptySelection())}>Clear</button>
  <span class="muted">limit {MAX_TAKES} drafts per batch</span>
</div>
{#if estimate.note}<p class="muted" data-testid="estimate-note">{estimate.note}</p>{/if}
{#if note}<p role="status" data-testid="board-note">{note}</p>{/if}
{#if assigning}<MusePicker onpick={assign} oncancel={() => (assigning = false)} />{/if}

{#if loading && !board}
  <p class="muted">Loading…</p>
{:else if board}
  {#if board.rows.length === 0}
    <p class="muted" data-testid="board-empty">Nothing on the Board for these filters.</p>
  {:else}
    <BoardGrid {grid} {selection} {onchip} {loadingMore} onmore={(r) => void more(null, r.nextUnassignedCursor)} />
  {/if}
  <p class="muted totals" data-testid="board-totals">
    {board.totals.muses} Muses · {board.totals.songs} songs · {board.totals.draft} draft · {board.totals.rendered} rendered · {board.totals.judged} judged · {board.totals.picked} picked
  </p>
  {#if board.next_cursor}
    <button type="button" disabled={loadingMore} onclick={() => void more(board?.next_cursor ?? null)} data-testid="board-more">Load more</button>
  {/if}
{/if}

<SessionPanel {sessions} onupdate={(s) => (sessions = sessions.map((x) => (x.id === s.id ? s : x)))} />

<style>
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: center;
    margin: 0.5rem 0;
  }
  .totals {
    font-size: 0.8rem;
  }
</style>
