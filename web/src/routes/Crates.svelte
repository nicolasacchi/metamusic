<script lang="ts">
  import { routeReady } from "../lib/scrollRestore";
  import { api } from "../api/client";
  import FilterBuilder from "../lib/components/FilterBuilder.svelte";
  import { createCrate, listCrates, type CrateSummary } from "../lib/crateApi";
  import { describeFilter, type CrateFilter, type FilterErrors, type Subject } from "../lib/crateFilter";

  interface Props {
    navigate?: (to: string, replace?: boolean) => void;
    /** Test seam: a first page without fetching. */
    initial?: CrateSummary[];
  }
  let { navigate, initial }: Props = $props();
  function go(to: string) {
    if (navigate) navigate(to);
    else {
      history.pushState(null, "", to);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }
  }

  // svelte-ignore state_referenced_locally
  let crates = $state<CrateSummary[]>(initial ?? []);
  let next = $state<string | null>(null);
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);
  let error = $state("");
  let showHidden = $state(false);
  let subjectFilter = $state<"" | Subject>("");
  let q = $state("");
  let seq = 0;

  async function load(more: boolean) {
    const mine = ++seq;
    loading = true;
    error = "";
    const r = await listCrates(api, { counts: true, includeHidden: showHidden, ...(subjectFilter ? { subject: subjectFilter } : {}), ...(q.trim() ? { q: q.trim() } : {}), ...(more && next ? { cursor: next } : {}) });
    if (mine !== seq) return;
    loading = false;
    if (r.ok) {
      crates = more ? [...crates, ...r.value.items] : r.value.items;
      next = r.value.next;
    } else error = r.message;
    void routeReady();
  }
  $effect(() => {
    void showHidden;
    void subjectFilter;
    void q;
    if (initial) return;
    void load(false);
  });

  // ---- New Crate ----
  let creating = $state(false);
  let name = $state("");
  let kind = $state<"manual" | "smart">("manual");
  let subject = $state<Subject>("song");
  let filter = $state<CrateFilter>({});
  let errors = $state<FilterErrors>({});
  let busy = $state(false);
  let formError = $state("");
  let action: ReturnType<typeof createCrate> | null = null;
  let actionFor = "";

  async function create() {
    const n = name.trim();
    if (!n || busy || (kind === "smart" && Object.keys(errors).length > 0)) return;
    busy = true;
    formError = "";
    // A retry of the same form reuses the Idempotency-Key; any edit starts a new action.
    const sig = JSON.stringify([n, kind, subject, kind === "smart" ? filter : null]);
    if (!action || actionFor !== sig) {
      action = createCrate(api, { name: n, kind, subject, ...(kind === "smart" ? { filter } : {}) });
      actionFor = sig;
    }
    const r = await action();
    busy = false;
    if (r.ok) {
      action = null;
      go(`/crates/${encodeURIComponent(r.value.crate.id)}`);
    } else formError = r.message;
  }
</script>

<h1>Crates</h1>
{#if error}<p role="alert">{error}</p>{/if}

<div class="bar">
  <button type="button" class="primary" aria-expanded={creating} onclick={() => (creating = !creating)} data-testid="crate-new">New Crate</button>
  <label><span class="sr">Search</span><input type="search" bind:value={q} placeholder="Search Crates" aria-label="Search Crates" data-testid="crate-search" /></label>
  <label><span class="sr">Holds</span>
    <select bind:value={subjectFilter} aria-label="Holds" data-testid="crate-subject-filter">
      <option value="">Songs and Takes</option>
      <option value="song">Songs</option>
      <option value="take">Takes</option>
    </select>
  </label>
  <label class="check"><input type="checkbox" bind:checked={showHidden} data-testid="crate-show-hidden" /> Show hidden</label>
</div>

{#if creating}
  <form
    class="new"
    data-testid="crate-new-form"
    onsubmit={(e) => {
      e.preventDefault();
      void create();
    }}
  >
    <label>Name <input bind:value={name} maxlength="80" required data-testid="crate-name" /></label>
    <div class="seg" role="radiogroup" aria-label="Kind">
      <label class="check"><input type="radio" bind:group={kind} value="manual" data-testid="crate-kind-manual" /> Manual (pick members)</label>
      <label class="check"><input type="radio" bind:group={kind} value="smart" data-testid="crate-kind-smart" /> Smart (saved filter)</label>
    </div>
    <div class="seg" role="radiogroup" aria-label="Holds">
      <label class="check"><input type="radio" bind:group={subject} value="song" data-testid="crate-subject-song" /> Songs</label>
      <label class="check"><input type="radio" bind:group={subject} value="take" data-testid="crate-subject-take" /> Takes</label>
    </div>
    {#if kind === "smart"}
      {#key subject}
        <FilterBuilder {subject} value={{}} onchange={(f, e) => ((filter = f), (errors = e))} />
      {/key}
    {/if}
    {#if formError}<p role="alert" data-testid="crate-form-error">{formError}</p>{/if}
    <button type="submit" class="primary" disabled={busy || !name.trim() || (kind === "smart" && Object.keys(errors).length > 0)} data-testid="crate-create">Create Crate</button>
  </form>
{/if}

{#if loading && crates.length === 0}
  <p class="muted">Loading…</p>
{:else if crates.length === 0}
  <p class="muted" data-testid="crates-empty">No Crates yet. A Crate is a working set: pick members by hand, or save a filter.</p>
{:else}
  <ul data-testid="crates-list">
    {#each crates as c (c.id)}
      <li data-testid="crate-row" data-id={c.id}>
        <a class="name" href="/crates/{encodeURIComponent(c.id)}">{c.name}</a>
        <span class="chips">
          <span class="chip">{c.kind}</span>
          <span class="chip">{c.subject === "song" ? "Songs" : "Takes"}</span>
          {#if c.hidden}<span class="chip">hidden</span>{/if}
        </span>
        <span class="muted" data-testid="crate-row-count">{c.count === null ? "" : `${c.count} ${c.subject === "song" ? (c.count === 1 ? "Song" : "Songs") : c.count === 1 ? "Take" : "Takes"}`}</span>
        {#if c.kind === "smart" && c.filter}<span class="muted desc">{describeFilter(c.subject, c.filter).join(" · ")}</span>{/if}
      </li>
    {/each}
  </ul>
  {#if next}<button type="button" disabled={loading} onclick={() => void load(true)} data-testid="crates-more">Load more</button>{/if}
{/if}

<style>
  .bar {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
    margin: var(--sp-2) 0;
  }
  .new {
    display: grid;
    gap: var(--sp-3);
    padding: var(--sp-3);
    margin: var(--sp-3) 0;
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    background: var(--surface);
  }
  .seg {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
  }
  .check {
    display: inline-flex;
    gap: var(--sp-2);
    align-items: center;
    min-height: var(--hit);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  li {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
    min-height: 64px;
    padding: var(--sp-2) var(--sp-3);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    background: var(--surface);
  }
  .name {
    flex: 1 1 10rem;
    display: inline-flex;
    align-items: center;
    min-height: var(--hit);
    font-weight: var(--fw-medium);
  }
  .chips {
    display: flex;
    gap: var(--sp-1);
  }
  .chip {
    padding: 0 var(--sp-2);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-pill);
    font-size: var(--fs-xs);
  }
  .desc {
    flex-basis: 100%;
    font-size: var(--fs-sm);
  }
  .sr {
    position: absolute;
    left: -9999px;
  }
</style>
