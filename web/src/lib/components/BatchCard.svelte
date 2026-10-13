<script lang="ts">
  import type { components } from "../../api/schema";
  import { canCancelBatch, type Viewer } from "../jobState";
  import { formatMicroUSD, formatMicroUSDCeil } from "../money";

  type Batch = components["schemas"]["BatchSummary"];
  interface Props {
    batch: Batch;
    viewer: Viewer;
    busy?: boolean;
    onterminate: (id: string) => void;
  }
  let { batch, viewer, busy = false, onterminate }: Props = $props();
  const c = $derived(batch.counts);
</script>

<article class="card" data-testid="batch-card">
  <h3><a href="/batches/{batch.id}">Batch of {batch.takes} take{batch.takes === 1 ? "" : "s"}</a> <span class="muted">{batch.state}</span></h3>
  <p class="muted">
    {c.completed} done · {c.running} running · {c.queued} queued{#if c.awaiting} · {c.awaiting} awaiting{/if}{#if c.failed} · {c.failed} failed{/if}{#if c.cancelled} · {c.cancelled} cancelled{/if}{#if c.expired} · {c.expired} expired{/if}{#if c.truncated} · {c.truncated} truncated{/if}
  </p>
  <p data-testid="batch-cost">
    Settled {formatMicroUSD(batch.cost.settled_micro_usd)} · outstanding {formatMicroUSD(batch.cost.outstanding_micro_usd)} · expected {formatMicroUSD(batch.cost.expected_micro_usd)} · up to {formatMicroUSDCeil(batch.cost.ceiling_micro_usd)}
  </p>
  <p class="muted">Approval: {batch.approval.state} · by {batch.created_by.name}</p>
  {#if canCancelBatch(batch, viewer)}
    <button type="button" disabled={busy} onclick={() => onterminate(batch.id)} data-testid="terminate-batch">Terminate batch</button>
  {/if}
</article>

<style>
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.4rem;
    padding: 0.5rem 0.8rem;
    margin: 0.5rem 0;
  }
  h3 {
    margin: 0 0 0.3rem;
  }
  p {
    margin: 0.2rem 0;
  }
</style>
