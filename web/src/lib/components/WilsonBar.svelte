<script lang="ts">
  // One critic score as a band: the 95% Wilson interval of "agrees with the Owner", the observed rate, and the two rule
  // lines (below the left one the critic is contrary, above the right one it can be trusted). Read-only.
  import { ariaFor, band, type Rule, type Score } from "../benchView";

  interface Props {
    label: string;
    score: Score;
    rule: Rule;
  }
  let { label, score, rule }: Props = $props();
  const b = $derived(band(score, rule));
</script>

<div class="wb" data-testid="wilson-bar" data-state={score.state}>
  <span class="lab">{label}</span>
  <div class="track" role="img" aria-label={ariaFor(label, score)}>
    <div class="band" style="left: {b.lo}%; width: {b.width}%"></div>
    <div class="rule" style="left: {b.contraryAt}%"></div>
    <div class="rule" style="left: {b.trustedAt}%"></div>
    <div class="rate" style="left: {b.rate}%"></div>
  </div>
  <span class="num">{score.agree}/{score.pairs}</span>
</div>

<style>
  .wb {
    display: grid;
    grid-template-columns: 6.5rem 1fr auto;
    align-items: center;
    gap: var(--sp-2);
    font-size: var(--fs-sm);
  }
  .lab {
    color: var(--text-2);
  }
  .track {
    position: relative;
    height: 0.75rem;
    min-width: 6rem;
    background: var(--bg-sunken);
    border: 1px solid var(--line);
    border-radius: var(--r-sm);
  }
  .band {
    position: absolute;
    top: 0;
    bottom: 0;
    background: var(--accent-soft);
    border: 1px solid var(--accent);
    box-sizing: border-box;
  }
  .rule {
    position: absolute;
    top: -2px;
    bottom: -2px;
    width: 1px;
    background: var(--line-strong);
  }
  .rate {
    position: absolute;
    top: -3px;
    bottom: -3px;
    width: 3px;
    margin-left: -1px;
    background: var(--text);
  }
  .num {
    font-variant-numeric: tabular-nums;
    color: var(--text-2);
  }
  [data-state="trusted"] .band {
    background: color-mix(in srgb, var(--ok) 30%, transparent);
    border-color: var(--ok);
  }
</style>
