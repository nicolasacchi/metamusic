<script lang="ts">
  // /desk (More → Make): the candidate scores on the Composer's Desk, grouped by the take they were made for (DESK-W1,
  // C363.2). Owner and Editors only (the route is gated on list_plans). A plan is a score, not a take: nothing here plays or
  // costs; opening one shows its notation and the bar editor.
  import { untrack } from "svelte";
  import { api as defaultApi } from "../api/client";
  import { checkAbc, listPlans, type DeskFailure, type PlanSummary } from "../lib/deskApi";
  import { deskPlanHref, filtersFromQuery, filtersToQuery, groupByBase, noFilters, planFacts, planLabel, reportLines, verdictText, type DeskFilters } from "../lib/deskView";

  interface Props {
    /** The page's query string (`?job_id=` from the Batch page, `?base_track_id=` from a take). */
    query?: Record<string, string>;
    client?: typeof defaultApi;
    /** Pre-loaded first page (unit tests). */
    initial?: PlanSummary[];
  }
  let { query = {}, client = defaultApi, initial }: Props = $props();

  // svelte-ignore state_referenced_locally
  let filters = $state<DeskFilters>(query ? filtersFromQuery(query) : { ...noFilters });
  // svelte-ignore state_referenced_locally
  let items = $state<PlanSummary[]>(initial ?? []);
  // svelte-ignore state_referenced_locally
  let loaded = $state(initial !== undefined);
  let next = $state<string | null>(null);
  let failure = $state<DeskFailure | null>(null);
  let busy = $state(false);
  let seq = 0;

  // svelte-ignore state_referenced_locally
  let skipFirst = initial !== undefined;
  const groups = $derived(groupByBase(items));

  async function load(more = false) {
    const mine = ++seq;
    busy = true;
    const r = await listPlans(client, filtersToQuery(filters, more && next ? next : undefined));
    if (mine !== seq) return;
    busy = false;
    loaded = true;
    if (!r.ok) {
      failure = r;
      return;
    }
    failure = null;
    items = more ? [...items, ...r.value.items] : r.value.items;
    next = r.value.next;
  }
  $effect(() => {
    for (const k of Object.keys(noFilters) as (keyof DeskFilters)[]) void filters[k];
    if (skipFirst) {
      skipFirst = false;
      return;
    }
    untrack(() => void load());
  });
  // The score check: does this text read as ABC, and what shape is it (check_abc). Never stored.
  let checkText = $state("");
  let checking = $state(false);
  let report = $state<{ ok: boolean; lines: string[] } | null>(null);
  async function check() {
    if (checking || checkText.trim() === "") return;
    checking = true;
    const r = await checkAbc(client, checkText);
    checking = false;
    report = r.ok ? reportLines(r.value) : { ok: false, lines: [r.message] };
  }
  const set = <K extends keyof DeskFilters>(k: K, v: DeskFilters[K]) => (filters[k] = v);
  const scoped = $derived(filters.jobId !== "" || filters.batchId !== "" || filters.baseTrackId !== "");
</script>

<h1>Desk</h1>
<p class="muted intro">Candidate scores for your takes. Open one to read it as notation and edit its bars; nothing here costs money.</p>

<section class="filters" aria-label="Filters" data-testid="desk-filters">
  <label>Made by
    <select value={filters.source} onchange={(e) => set("source", e.currentTarget.value as DeskFilters["source"])} data-testid="desk-filter-source">
      <option value="">Any</option>
      <option value="take">A take's own score</option>
      <option value="job">A plan job</option>
      <option value="edit">An edit</option>
    </select>
  </label>
  <label>Your verdict
    <select value={filters.verdict} onchange={(e) => set("verdict", e.currentTarget.value as DeskFilters["verdict"])} data-testid="desk-filter-verdict">
      <option value="">Any</option>
      <option value="liked">Liked</option>
      <option value="rejected">Rejected</option>
      <option value="none">None yet</option>
    </select>
  </label>
  <label>Rendered
    <select value={filters.rendered} onchange={(e) => set("rendered", e.currentTarget.value as DeskFilters["rendered"])} data-testid="desk-filter-rendered">
      <option value="">Any</option>
      <option value="yes">Has a take</option>
      <option value="no">No take yet</option>
    </select>
  </label>
  {#if scoped}
    <button type="button" onclick={() => { filters.jobId = ""; filters.batchId = ""; filters.baseTrackId = ""; }} data-testid="desk-clear-scope">Show every plan</button>
  {/if}
</section>

<details class="card check" data-testid="desk-check">
  <summary>Check a score</summary>
  <label>Score (ABC notation)
    <textarea rows="6" spellcheck="false" value={checkText} oninput={(e) => (checkText = e.currentTarget.value)} data-testid="desk-check-abc"></textarea>
  </label>
  <button type="button" disabled={checking || checkText.trim() === ""} onclick={() => void check()} data-testid="desk-check-run">{checking ? "Checking…" : "Check"}</button>
  {#if report}
    <ul class={report.ok ? "ok" : "err"} role="status" data-testid="desk-check-report" data-ok={report.ok}>
      {#each report.lines as l, k (k)}<li>{l}</li>{/each}
    </ul>
  {/if}
</details>

{#if failure}
  <p class="err" role="alert" data-testid="desk-error">{failure.message}</p>
  <button type="button" onclick={() => void load()}>Try again</button>
{:else if !loaded}
  <p class="muted" data-testid="desk-loading">Loading…</p>
{:else if groups.length === 0}
  <p class="muted" data-testid="desk-empty">No plans yet. Open a take that has a score in the Desk from its page.</p>
{:else}
  <div class="groups" data-testid="desk-groups">
    {#each groups as g (g.key)}
      <section class="card group" data-testid="desk-group">
        <h2>{#if g.trackId}<a href="/tracks/{g.trackId}">{g.title}</a>{:else}{g.title}{/if}</h2>
        <ul>
          {#each g.plans as p (p.id)}
            <li data-testid="desk-plan" data-id={p.id}>
              <a href={deskPlanHref(p.id)} class="plan">
                <b>{planLabel(p)}</b>
                <span class="muted">{planFacts(p)}</span>
              </a>
              <span class="chips">
                {#if p.cot === "melody"}<span class="chip">melody</span>{/if}
                <span class="chip" data-testid="desk-verdict" data-verdict={p.verdict ?? ""}>{verdictText(p.verdict)}</span>
                {#if p.rendered > 0}<span class="chip">{p.rendered} take{p.rendered === 1 ? "" : "s"}</span>{/if}
              </span>
            </li>
          {/each}
        </ul>
      </section>
    {/each}
  </div>
  {#if next}<button type="button" disabled={busy} onclick={() => void load(true)} data-testid="desk-more">{busy ? "Loading…" : "Show more"}</button>{/if}
{/if}

<style>
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
    align-items: end;
    margin: var(--sp-3) 0;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
    min-width: 0;
  }
  select,
  button {
    min-height: var(--hit);
    max-width: 100%;
    font: inherit;
  }
  summary {
    display: flex;
    align-items: center;
    min-height: var(--hit);
    cursor: pointer;
  }
  .check {
    display: grid;
    gap: var(--sp-2);
    margin-bottom: var(--sp-3);
  }
  textarea {
    min-height: 6rem;
    max-width: 100%;
    box-sizing: border-box;
    font-family: var(--font-mono, monospace);
  }
  .groups {
    display: grid;
    gap: var(--sp-3);
    min-width: 0;
  }
  .group {
    min-width: 0;
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
    justify-content: space-between;
    min-width: 0;
  }
  .plan {
    display: flex;
    flex-direction: column;
    min-height: var(--hit);
    justify-content: center;
    min-width: 0;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-1);
  }
</style>
