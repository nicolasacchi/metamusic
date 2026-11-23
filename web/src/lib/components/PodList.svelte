<script lang="ts">
  import type { components } from "../../api/schema";
  import { podCostText, podLine, podTone } from "../bulkView";

  type Pod = components["schemas"]["PodSummary"];
  interface Props {
    pods: Pod[];
    /** What to say with no pods. */
    empty: string;
    now?: Date;
    /** Link each pod to its batch (the Jobs page). */
    batchLinks?: boolean;
  }
  let { pods, empty, now = new Date(), batchLinks = false }: Props = $props();
</script>

{#if pods.length === 0}
  <p class="muted" data-testid="pods-empty">{empty}</p>
{:else}
  <ul class="pods" data-testid="pod-list">
    {#each pods as p (p.id)}
      <li data-testid="pod-row" data-state={p.state} data-tone={podTone(p)}>
        <b>{p.name}</b>
        {#if p.kind === "canary"}<span class="muted">(canary)</span>{/if}
        <span>{podLine(p, now)}</span>
        <span data-testid="pod-cost">{podCostText(p)}</span>
        {#if p.gpu_try > 1}<span class="muted">create attempt {p.gpu_try}</span>{/if}
        {#if batchLinks}<a href="/batches/{p.batch_id}">batch</a>{/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .pods {
    list-style: none;
    padding: 0;
  }
  li {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 0.8rem;
    padding: 0.3rem 0;
    border-bottom: 1px solid #2a2e37;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  li[data-tone="bad"] {
    color: #ff8a80;
  }
  li[data-tone="ok"] b {
    color: #7bd88f;
  }
</style>
