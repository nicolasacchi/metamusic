<script lang="ts">
  import { api } from "../api/client";
  import type { components, paths } from "../api/schema";
  import Pager from "../lib/components/Pager.svelte";
  import { describeItem, summaryCounts } from "../lib/importItem";

  type Run = components["schemas"]["ImportRunSummary"];
  type Section = NonNullable<paths["/api/imports/{id}"]["get"]["parameters"]["query"]>["section"];

  const SECTIONS: NonNullable<Section>[] = ["errors", "warnings", "unmatched", "ambiguous", "audio", "recipes", "drafts", "duplicates", "ignored"];
  const PAGE = 50;

  let runs = $state<Run[]>([]);
  let runId = $state("");
  let section = $state<NonNullable<Section>>("errors");
  let items = $state<unknown[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state("");
  let cursors = $state<(string | undefined)[]>([undefined]);
  let starts = $state<number[]>([0]);
  let page = $state(0);
  let nextCursor = $state<string | null>(null);
  let seq = 0;

  const run = $derived(runs.find((r) => r.id === runId) ?? null);

  $effect(() => {
    void (async () => {
      try {
        const r = await api.GET("/api/imports", { params: { query: { limit: 20 } } });
        if (!r.data) {
          error = r.error?.message ?? "Failed to load import runs";
          loading = false;
          return;
        }
        runs = r.data.items;
        runId = runs[0]?.id ?? "";
        if (!runId) loading = false;
      } catch {
        error = "Failed to load import runs";
        loading = false;
      }
    })();
  });

  async function load(id: string, sec: NonNullable<Section>, cursor: string | undefined) {
    const mine = ++seq;
    loading = true;
    error = "";
    try {
      const r = await api.GET("/api/imports/{id}", {
        params: { path: { id }, query: { section: sec, limit: PAGE, ...(cursor ? { cursor } : {}) } },
      });
      if (mine !== seq) return;
      if (!r.data) {
        error = r.error?.message ?? "Failed to load section";
        return;
      }
      items = r.data.items;
      total = r.data.total;
      nextCursor = r.data.next_cursor;
    } catch {
      if (mine === seq) error = "Failed to load section";
    } finally {
      if (mine === seq) loading = false;
    }
  }

  $effect(() => {
    const id = runId;
    const sec = section;
    if (!id) return;
    cursors = [undefined];
    starts = [0];
    page = 0;
    nextCursor = null;
    void load(id, sec, undefined);
  });

  function go(delta: number) {
    const target = page + delta;
    if (target < 0) return;
    if (delta > 0) {
      if (!nextCursor) return;
      cursors[target] = nextCursor;
      starts[target] = (starts[page] ?? 0) + items.length;
    }
    page = target;
    void load(runId, section, cursors[target]);
  }
</script>

<h1>Import</h1>
{#if error}<p role="alert">{error}</p>{/if}

{#if runs.length > 1}
  <label>
    Run
    <select bind:value={runId} data-testid="import-run-select">
      {#each runs as r (r.id)}
        <option value={r.id}>{r.started_at} ({r.state})</option>
      {/each}
    </select>
  </label>
{/if}

{#if run}
  <section data-testid="import-summary">
    <h2>Latest run</h2>
    <p>
      <b data-testid="import-state">{run.state}</b>
      · started {run.started_at}{#if run.finished_at} · finished {run.finished_at}{/if}
    </p>
    <ul class="counts">
      <li data-testid="import-errors">errors: {run.errors}</li>
      <li data-testid="import-warnings">warnings: {run.warnings}</li>
      {#each summaryCounts(run.summary) as [k, n] (k)}
        <li data-count={k}>{k}: {n}</li>
      {/each}
    </ul>
  </section>

  <nav class="tabs" aria-label="Sections">
    {#each SECTIONS as s (s)}
      <button type="button" aria-pressed={section === s} onclick={() => (section = s)} data-section={s}>{s}</button>
    {/each}
  </nav>

  <div aria-busy={loading}>
    <ul class="items" data-testid="import-items" data-section={section}>
      {#each items as it, i (i)}
        <li>{describeItem(it)}</li>
      {:else}
        {#if !loading && !error}<li class="muted">Nothing in {section}.</li>{/if}
      {/each}
    </ul>
  </div>
  <Pager {page} offset={starts[page] ?? 0} count={items.length} {total} busy={loading} hasNext={nextCursor !== null} onprev={() => go(-1)} onnext={() => go(1)} />
{:else if !loading && !error}
  <p class="muted" data-testid="import-empty">No import has run yet.</p>
{/if}

<style>
  .counts {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem 1.25rem;
    list-style: none;
    padding: 0;
  }
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin: 1rem 0 0.5rem;
  }
  .tabs button[aria-pressed="true"] {
    font-weight: 700;
    outline: 2px solid #f2a65a;
  }
  .items {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .items li {
    padding: 0.3rem 0;
    border-bottom: 1px solid #2a241f;
    overflow-wrap: anywhere;
    font-size: 0.85rem;
  }
</style>
