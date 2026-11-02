<script lang="ts">
  import { basisWord, isHollow, keyText, type KeyInfo } from "../featuresView";

  interface Props {
    musicalKey: KeyInfo | null | undefined;
  }
  let { musicalKey }: Props = $props();
  const text = $derived(keyText(musicalKey));
</script>

{#if text}
  <span class="badge" class:hollow={isHollow(musicalKey?.basis)} data-testid="key-badge" data-basis={musicalKey?.basis ?? ""} title="Key, {basisWord(musicalKey?.basis)}">
    <span class="mark" aria-hidden="true"></span>{text}
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
