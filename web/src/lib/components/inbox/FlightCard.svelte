<script lang="ts">
  import { flightSummary, type FlightBatch } from "../../inbox";

  interface Props {
    batch: FlightBatch;
    cost?: string;
  }
  let { batch, cost = "" }: Props = $props();
</script>

<article class="card" data-testid="flight-card" data-batch={batch.id}>
  <h3><a href="/batches/{batch.id}">Batch · {batch.segments.length} take{batch.segments.length === 1 ? "" : "s"} · {batch.title}</a></h3>
  <ol class="segs" aria-label="Takes">
    {#each batch.segments as s (s.jobId)}
      <li class="seg {s.state}" data-testid="flight-seg" data-state={s.state}>
        <span class="sr">{s.state}</span>
      </li>
    {/each}
  </ol>
  <p class="muted" data-testid="flight-summary">{flightSummary(batch)}{cost ? ` · ${cost}` : ""}</p>
</article>

<style>
  .card {
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    padding: var(--sp-3);
    margin-bottom: var(--sp-3);
    background: var(--surface);
  }
  h3 {
    margin: 0 0 var(--sp-2);
    font-size: var(--fs-md);
  }
  .segs {
    display: flex;
    gap: var(--sp-1);
    list-style: none;
    margin: 0 0 var(--sp-2);
    padding: 0;
  }
  .seg {
    flex: 1;
    min-height: 28px;
    border-radius: var(--r-sm);
    background: var(--surface-2);
    overflow: hidden;
  }
  .seg.running {
    background: var(--accent-soft);
    outline: 1px solid var(--accent);
  }
  .seg.done {
    background: var(--ok);
  }
  .seg.failed {
    background: #5a2a22;
  }
  .sr {
    position: absolute;
    left: -9999px;
  }
  p {
    margin: 0;
    font-size: var(--fs-sm);
  }
</style>
