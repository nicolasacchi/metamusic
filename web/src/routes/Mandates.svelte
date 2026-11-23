<script lang="ts">
  import { api } from "../api/client";
  import MandateRevoke from "../lib/components/MandateRevoke.svelte";
  import { formatMicroUSD as usd } from "../lib/money";
  import { can } from "../lib/me.svelte";
  import { canRevoke, scopeSummary, stateLabel, whenText, type Mandate } from "../lib/mandateView";

  let items = $state<Mandate[]>([]);
  let cursor = $state<string | null>(null);
  let loaded = $state(false);
  let error = $state("");
  let more = $state(false);

  async function load(next?: string) {
    more = !!next;
    try {
      const r = await api.GET("/api/mandates", { params: { query: { limit: 50, ...(next ? { cursor: next } : {}) } } });
      if (r.data) {
        items = next ? [...items, ...r.data.items] : r.data.items;
        cursor = r.data.next_cursor ?? null;
        error = "";
      } else error = r.error?.message ?? "Could not load the mandates";
    } catch {
      error = "Could not load the mandates";
    } finally {
      loaded = true;
      more = false;
    }
  }
  $effect(() => {
    void load();
  });

  function revoked(m: Mandate) {
    items = items.map((x) => (x.id === m.id ? m : x));
  }
</script>

<section data-testid="mandates-page">
  <h1>Mandates</h1>
  <p class="muted">A mandate is the only thing that lets the night shift spend without a click: a budget, a window and a scope. It never raises a cap.</p>
  {#if can("create_mandate")}
    <p><a class="btn" href="/human/mandates/new" data-testid="mandate-new-link">New mandate</a></p>
  {/if}
  {#if error}<p role="alert" data-testid="mandates-error">{error}</p>{/if}
  {#if !loaded}
    <p class="muted" role="status">Loading…</p>
  {:else if items.length === 0 && !error}
    <p class="muted" data-testid="mandates-empty">No mandates yet. The night shift cannot spend anything without one.</p>
  {:else}
    <ul class="list">
      {#each items as m (m.id)}
        <li data-testid="mandate-row" data-state={m.effective_state}>
          <div class="head">
            <a href="/mandates/{m.id}" class="title">{m.title}</a>
            <span class="chip {m.effective_state}">{stateLabel(m.effective_state)}</span>
          </div>
          <p class="muted">{usd(m.budget_micro_usd)} budget, {usd(m.usage.used_micro_usd)} used, {usd(m.usage.remaining_micro_usd)} left. {whenText(m.valid_from)} to {whenText(m.valid_until)}.</p>
          <p class="muted">{scopeSummary(m.scope)}</p>
          {#if canRevoke(m)}<MandateRevoke mandate={m} onrevoked={revoked} />{/if}
        </li>
      {/each}
    </ul>
    {#if cursor}<button type="button" disabled={more} onclick={() => void load(cursor ?? undefined)}>Show more</button>{/if}
  {/if}
</section>

<style>
  .list {
    list-style: none;
    margin: 0 0 var(--sp-4);
    padding: 0;
    display: grid;
    gap: var(--sp-3);
  }
  li {
    padding: var(--sp-3) var(--sp-4);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
  }
  .head {
    display: flex;
    justify-content: space-between;
    gap: var(--sp-3);
    align-items: center;
  }
  .title {
    font-weight: var(--fw-medium);
  }
  p {
    margin: var(--sp-1) 0;
  }
  .chip {
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    font-size: var(--fs-xs);
    background: var(--surface-2);
    color: var(--text-2);
  }
  .chip.live {
    background: color-mix(in srgb, var(--ok) 25%, transparent);
    color: var(--ok);
  }
  .chip.revoked {
    background: var(--bad-soft);
    color: var(--bad);
  }
  .btn {
    display: inline-flex;
    align-items: center;
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: 1px solid var(--accent);
    border-radius: var(--r-pill);
    color: var(--accent);
    text-decoration: none;
  }
</style>
