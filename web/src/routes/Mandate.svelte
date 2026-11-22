<script lang="ts">
  import { api } from "../api/client";
  import MandateRevoke from "../lib/components/MandateRevoke.svelte";
  import { formatMicroUSD } from "../lib/money";
  import { canRevoke, scopeSummary, stateLabel, usageBar, whenText, type Mandate } from "../lib/mandateView";

  let { id }: { id: string } = $props();
  let m = $state<Mandate | null>(null);
  let error = $state("");

  async function load() {
    try {
      const r = await api.GET("/api/mandates/{id}", { params: { path: { id } } });
      if (r.data) {
        m = r.data;
        error = "";
      } else error = r.error?.message ?? "Could not load the mandate";
    } catch {
      error = "Could not load the mandate";
    }
  }
  $effect(() => {
    void load();
  });
  const bar = $derived(m ? usageBar(m.usage, m.budget_micro_usd) : null);
</script>

<section data-testid="mandate-page">
  {#if error}
    <h1>Mandate</h1>
    <p role="alert" data-testid="mandate-error">{error}</p>
  {:else if !m || !bar}
    <h1>Mandate</h1>
    <p class="muted" role="status">Loading…</p>
  {:else}
    <h1>{m.title}</h1>
    <p>
      <span class="chip {m.effective_state}" data-testid="mandate-state">{stateLabel(m.effective_state)}</span>
      <span class="muted">{whenText(m.valid_from)} to {whenText(m.valid_until)}</span>
    </p>
    <p class="muted">{scopeSummary(m.scope)}. Ceiling per night {formatMicroUSD(m.scope.max_batch_ceiling_micro_usd)}.</p>

    <h2>Usage</h2>
    <div class="track" role="img" aria-label="Settled {bar.settledPct}%, reserved {bar.reservedPct}%, left {bar.remainingPct}% of {formatMicroUSD(m.budget_micro_usd)}" data-testid="mandate-usage-bar">
      <div class="settled" style="width: {bar.settledPct}%"></div>
      <div class="reserved" style="width: {bar.reservedPct}%"></div>
    </div>
    <dl data-testid="mandate-usage">
      <dt>Budget</dt><dd>{formatMicroUSD(m.budget_micro_usd)}</dd>
      <dt>Settled</dt><dd>{formatMicroUSD(m.usage.settled_micro_usd)}</dd>
      <dt>Reserved</dt><dd>{formatMicroUSD(m.usage.reserved_micro_usd)}</dd>
      <dt>Left</dt><dd>{formatMicroUSD(m.usage.remaining_micro_usd)}</dd>
      {#if m.usage.excess_micro_usd > 0}<dt>Over the mandate (reported apart)</dt><dd>{formatMicroUSD(m.usage.excess_micro_usd)}</dd>{/if}
      <dt>Batches / takes</dt><dd>{m.usage.batches} / {m.usage.takes}</dd>
    </dl>
    {#if (m.usage.by_root ?? []).length > 0}
      <h2>Takes per lineage</h2>
      <ul>
        {#each m.usage.by_root ?? [] as r (r.root_track_id)}
          <li><a href="/lineage/{r.root_track_id}">{r.root_track_id}</a>: {r.takes}</li>
        {/each}
      </ul>
    {/if}

    {#if m.revoked}
      <p data-testid="mandate-revoked">Revoked {whenText(m.revoked.at)} by {m.revoked.by.name}: {m.revoked.reason}</p>
    {:else if canRevoke(m)}
      <MandateRevoke mandate={m} onrevoked={(x) => (m = x)} />
    {/if}
    <p><a href="/mandates">All mandates</a></p>
  {/if}
</section>

<style>
  .track {
    display: flex;
    height: 0.75rem;
    background: var(--bg-sunken);
    border: 1px solid var(--line);
    border-radius: var(--r-sm);
    overflow: hidden;
  }
  .settled {
    background: var(--accent);
  }
  .reserved {
    background: var(--accent-soft);
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--sp-1) var(--sp-4);
  }
  dt {
    color: var(--text-2);
  }
  dd {
    margin: 0;
  }
  .chip {
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    font-size: var(--fs-xs);
    background: var(--surface-2);
  }
  .chip.live {
    background: color-mix(in srgb, var(--ok) 25%, transparent);
    color: var(--ok);
  }
  .chip.revoked {
    background: var(--bad-soft);
    color: var(--bad);
  }
</style>
