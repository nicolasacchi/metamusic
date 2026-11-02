<script lang="ts">
  import { basisWord, energyText, isHollow, type EnergyInfo } from "../featuresView";

  interface Props {
    energy: EnergyInfo | null | undefined;
  }
  let { energy }: Props = $props();
  const text = $derived(energyText(energy));
</script>

{#if text}
  <span class="badge" class:hollow={isHollow(energy?.basis)} data-testid="energy-badge" data-basis={energy?.basis ?? ""} title="Energy, {basisWord(energy?.basis)}">
    <span class="mark" aria-hidden="true"></span>Energy {text}
  </span>
{/if}

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-1);
    padding: 0 var(--sp-2);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    font-size: var(--fs-sm);
    white-space: nowrap;
  }
  .mark {
    width: 8px;
    height: 8px;
    border-radius: var(--r-pill);
    border: 1px solid var(--accent);
    background: var(--accent);
  }
  .hollow .mark {
    background: transparent;
  }
</style>
