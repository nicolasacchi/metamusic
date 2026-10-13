<script lang="ts">
  // "Variant ▾" menu on the Track page (C84.1). Hidden for devices, principals without the generate scope
  // and tracks whose recipe is unknown or whose master is lossy (RecipeUnknownPanel explains why).
  import { canGenerate, startGeneration, variantIntent, type Viewer, type VariantMode } from "../genIntent";

  interface Props {
    trackId: string;
    recipeKnown: boolean;
    lossy: boolean;
    me: Viewer | null;
    /** Defaults to a full-page navigation (the /human/generate page reads the intent from sessionStorage). */
    go?: (to: string) => void;
  }
  let { trackId, recipeKnown, lossy, me, go }: Props = $props();

  const items: { mode: VariantMode; label: string }[] = [
    { mode: "new_seed", label: "New seed ×3" },
    { mode: "lyric_edit", label: "Edit lyrics" },
    { mode: "style_edit", label: "Edit style/BPM" },
    { mode: "same_seed_edit", label: "Same seed edit" },
  ];

  function pick(mode: VariantMode) {
    startGeneration(sessionStorage, variantIntent(trackId, mode), go ?? ((to) => location.assign(to)));
  }
</script>

{#if canGenerate(me, { recipe_known: recipeKnown, lossy })}
  <details class="gen" data-testid="generate-button">
    <summary>Variant ▾</summary>
    <ul role="menu">
      {#each items as it (it.mode)}
        <li role="none"><button type="button" role="menuitem" data-testid="variant-{it.mode}" onclick={() => pick(it.mode)}>{it.label}</button></li>
      {/each}
    </ul>
  </details>
{/if}

<style>
  .gen {
    display: inline-block;
    position: relative;
  }
  summary {
    cursor: pointer;
    padding: 0.25rem 0.6rem;
    border: 1px solid #8a7f74;
    border-radius: 0.3rem;
    user-select: none;
  }
  ul {
    list-style: none;
    margin: 0.2rem 0 0;
    padding: 0.3rem;
    position: absolute;
    z-index: 5;
    background: #26211c;
    border: 1px solid #3a322b;
    border-radius: 0.4rem;
    min-width: 11rem;
  }
  li button {
    width: 100%;
    text-align: left;
  }
</style>
