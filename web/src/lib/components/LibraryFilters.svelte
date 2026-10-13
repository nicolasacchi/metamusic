<script lang="ts">
  import type { components } from "../../api/schema";
  import { MAX_STYLE_TERMS, defaultSort, SORTS, STATUSES, type Filters, type Sort } from "../libraryQuery";

  type Facets = components["schemas"]["Facets"];
  interface Props {
    filters: Filters;
    facets: Facets | null;
    onchange: (f: Filters) => void;
  }
  let { filters, facets, onchange }: Props = $props();

  const SORT_LABELS: Record<Sort, string> = {
    recorded_desc: "Newest first",
    recorded_asc: "Oldest first",
    duration_desc: "Longest first",
    duration_asc: "Shortest first",
    title_asc: "Title A–Z",
    relevance: "Relevance",
    stars_desc: "Top rated",
  };

  // Free-text input is debounced; every other control commits immediately.
  let qDraft = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;
  let pending = false;
  let lastQ: string | undefined;
  // Re-sync the box only when the URL's q itself changed (back/forward, Clear), never on other filters.
  $effect(() => {
    const q = filters.q;
    if (q !== lastQ) {
      lastQ = q;
      qDraft = q;
      pending = false;
      clearTimeout(timer);
    }
  });
  $effect(() => () => clearTimeout(timer));

  /** The sort follows q only while it is the implicit default for the old q. */
  function withQ(f: Filters, q: string): Filters {
    const sort = f.sort === defaultSort(f.q) ? defaultSort(q) : f.sort;
    return { ...f, q, sort };
  }
  function flushQ(base: Filters): Filters {
    if (!pending) return base;
    pending = false;
    clearTimeout(timer);
    const q = qDraft.trim();
    return q === base.q ? base : withQ(base, q);
  }
  function patch(p: Partial<Filters>) {
    onchange({ ...flushQ(filters), ...p });
  }
  function onq() {
    pending = true;
    clearTimeout(timer);
    timer = setTimeout(() => {
      pending = false;
      const q = qDraft.trim();
      if (q === filters.q) return;
      onchange(withQ(filters, q));
    }, 250);
  }
  function toggleStyle(term: string) {
    const on = filters.style.includes(term);
    if (!on && filters.style.length >= MAX_STYLE_TERMS) return;
    patch({ style: on ? filters.style.filter((s) => s !== term) : [...filters.style, term] });
  }
  function toggleStatus(s: (typeof STATUSES)[number]) {
    patch({ status: filters.status.includes(s) ? filters.status.filter((x) => x !== s) : [...filters.status, s] });
  }
  function minutes(e: Event): number | null {
    const v = (e.currentTarget as HTMLInputElement).value.trim();
    const n = Number(v);
    return v === "" || !Number.isFinite(n) || n < 0 ? null : n * 60;
  }
  function tri(e: Event): boolean | null {
    const v = (e.currentTarget as HTMLSelectElement).value;
    return v === "" ? null : v === "true";
  }
  const dirty = $derived(
    filters.q !== "" ||
      filters.style.length > 0 ||
      filters.status.length > 0 ||
      filters.minDuration !== null ||
      filters.maxDuration !== null ||
      filters.recordedFrom !== "" ||
      filters.recordedTo !== "" ||
      filters.hasScore !== null ||
      filters.truncated !== null,
  );
</script>

<form class="filters" role="search" aria-label="Filter tracks" onsubmit={(e) => e.preventDefault()}>
  <div class="row">
    <label>
      Search
      <input type="search" name="q" data-testid="filter-q" bind:value={qDraft} oninput={onq} maxlength="200" placeholder="title, style, lyrics" />
    </label>
    <label>
      Sort
      <select name="sort" value={filters.sort} onchange={(e) => patch({ sort: e.currentTarget.value as Sort })}>
        {#each SORTS as s (s)}
          {#if s !== "relevance" || filters.q}<option value={s}>{SORT_LABELS[s]}</option>{/if}
        {/each}
      </select>
    </label>
    <label>
      Min (min)
      <input type="number" min="0" step="0.5" name="min" value={filters.minDuration === null ? "" : filters.minDuration / 60} onchange={(e) => patch({ minDuration: minutes(e) })} />
    </label>
    <label>
      Max (min)
      <input type="number" min="0" step="0.5" name="max" value={filters.maxDuration === null ? "" : filters.maxDuration / 60} onchange={(e) => patch({ maxDuration: minutes(e) })} />
    </label>
    <label>
      From
      <input type="date" name="from" value={filters.recordedFrom} onchange={(e) => patch({ recordedFrom: e.currentTarget.value })} />
    </label>
    <label>
      To
      <input type="date" name="to" value={filters.recordedTo} onchange={(e) => patch({ recordedTo: e.currentTarget.value })} />
    </label>
    <label>
      Score
      <select name="has_score" value={filters.hasScore === null ? "" : String(filters.hasScore)} onchange={(e) => patch({ hasScore: tri(e) })}>
        <option value="">any</option>
        <option value="true">has score</option>
        <option value="false">no score</option>
      </select>
    </label>
    <label>
      Truncated
      <select name="truncated" value={filters.truncated === null ? "" : String(filters.truncated)} onchange={(e) => patch({ truncated: tri(e) })}>
        <option value="">any</option>
        <option value="true">truncated</option>
        <option value="false">complete</option>
      </select>
    </label>
    {#if dirty}
      <button type="button" onclick={() => { pending = false; clearTimeout(timer); onchange({ ...filters, q: "", status: [], style: [], minDuration: null, maxDuration: null, recordedFrom: "", recordedTo: "", hasScore: null, truncated: null, sort: "recorded_desc" }); }}>Clear</button>
    {/if}
  </div>

  <fieldset>
    <legend>Status</legend>
    {#each STATUSES as s (s)}
      <label class="check"><input type="checkbox" checked={filters.status.includes(s)} onchange={() => toggleStatus(s)} /> {s}</label>
    {/each}
  </fieldset>

  {#if facets && (facets.style_terms.length > 0 || filters.style.length > 0)}
    <fieldset>
      <legend>Style (all selected must match, max {MAX_STYLE_TERMS})</legend>
      <div class="terms" data-testid="style-facets">
        {#each filters.style.filter((s) => !facets.style_terms.some((t) => t.term === s)) as term (term)}
          <button type="button" class="term on" aria-pressed="true" onclick={() => toggleStyle(term)}>{term}</button>
        {/each}
        {#each facets.style_terms as t (t.term)}
          {@const on = filters.style.includes(t.term)}
          <button type="button" class="term" class:on aria-pressed={on} onclick={() => toggleStyle(t.term)} disabled={!on && filters.style.length >= MAX_STYLE_TERMS}>
            {t.term} <span class="muted">{t.count}</span>
          </button>
        {/each}
      </div>
    </fieldset>
  {/if}
</form>

<style>
  .filters {
    display: grid;
    gap: 0.6rem;
    margin-bottom: 1rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
    align-items: end;
  }
  label {
    display: grid;
    gap: 0.15rem;
    font-size: 0.8rem;
  }
  label.check {
    display: inline-flex;
    gap: 0.3rem;
    align-items: center;
    margin-right: 0.8rem;
    font-size: 0.9rem;
  }
  input[type="number"] {
    width: 5rem;
  }
  fieldset {
    border: 1px solid #3a322b;
    border-radius: 0.4rem;
    padding: 0.3rem 0.6rem;
  }
  legend {
    font-size: 0.8rem;
    opacity: 0.7;
  }
  .terms {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
  }
  .term {
    border-radius: 1rem;
    padding: 0.2rem 0.7rem;
    min-height: 2rem;
  }
  .term.on {
    background: #f2a65a;
    color: #14110f;
  }
</style>
