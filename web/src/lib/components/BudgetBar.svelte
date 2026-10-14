<script lang="ts">
  import { formatMicroUSD } from "../money";

  interface Props {
    label: string;
    settled: number;
    /** Omit when the figure is a single "used" total (reserved not split out). */
    reserved?: number;
    cap: number;
  }
  let { label, settled, reserved, cap }: Props = $props();
  const pct = (n: number) => (cap > 0 ? Math.min(100, Math.max(0, (n / cap) * 100)) : 0);
  const s = $derived(pct(settled));
  const r = $derived(Math.min(100 - s, pct(reserved ?? 0)));
</script>

<div class="bar" data-testid="budget-bar">
  <div class="head">
    <span>{label}</span>
    <span>{reserved === undefined ? `${formatMicroUSD(settled)} used` : `${formatMicroUSD(settled)} settled + ${formatMicroUSD(reserved)} reserved`} of {formatMicroUSD(cap)}</span>
  </div>
  <div class="track" role="img" aria-label="{label}: {formatMicroUSD(settled + (reserved ?? 0))} of {formatMicroUSD(cap)}">
    <div class="settled" style="width: {s}%"></div>
    <div class="reserved" style="width: {r}%"></div>
  </div>
</div>

<style>
  .bar {
    margin: 0.4rem 0;
  }
  .head {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    font-size: 0.85rem;
  }
  .track {
    display: flex;
    height: 0.5rem;
    background: #2a231d;
    border-radius: 0.25rem;
    overflow: hidden;
  }
  .settled {
    background: #f2a65a;
  }
  .reserved {
    background: #8a6a45;
  }
</style>
