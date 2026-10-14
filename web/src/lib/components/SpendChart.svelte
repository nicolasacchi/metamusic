<script lang="ts">
  import { formatMicroUSD } from "../money";

  interface Day {
    date: string;
    settled_micro_usd: number;
  }
  interface Props {
    days: Day[];
  }
  let { days }: Props = $props();
  // Bar heights are proportions of integer micro-USD; the labels are formatted from the integers.
  const max = $derived(days.reduce((m, d) => Math.max(m, d.settled_micro_usd), 0));
  const W = 16;
  const H = 80;
</script>

{#if days.length === 0 || max === 0}
  <p class="muted" data-testid="spend-empty">No spend this month.</p>
{:else}
  <svg role="img" aria-label="Settled spend per day" viewBox="0 0 {days.length * W} {H}" width="100%" height="120" preserveAspectRatio="none">
    {#each days as d, i (d.date)}
      {@const h = Math.max(1, Math.round((d.settled_micro_usd / max) * H))}
      <rect data-testid="spend-bar" x={i * W + 2} y={H - h} width={W - 4} height={h} fill="#f2a65a">
        <title>{d.date}: {formatMicroUSD(d.settled_micro_usd)}</title>
      </rect>
    {/each}
  </svg>
{/if}
