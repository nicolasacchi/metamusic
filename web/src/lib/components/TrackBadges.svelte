<script lang="ts">
  // "Recipe unknown" / "Lossy MP3" chips (M2c C64). Text, never colour alone; the title explains.
  interface Props {
    recipeKnown: boolean;
    lossy: boolean;
  }
  let { recipeKnown, lossy }: Props = $props();
</script>

{#if !recipeKnown}<span class="badge unknown" data-testid="chip-recipe-unknown" title="No recipe exists for this track; it cannot be re-rendered or used as a mother">Recipe unknown</span>{/if}
{#if lossy}<span class="badge lossy" data-testid="chip-lossy" title="The master is an MP3; there is no lossless file">Lossy MP3</span>{/if}

<style>
  .badge {
    display: inline-block;
    font-size: 0.75rem;
    padding: 0 0.4rem;
    margin: 0 0.25rem 0.15rem 0;
    border-radius: 0.6rem;
    background: transparent;
    border: 1px solid #8a7f74;
    white-space: nowrap;
  }
  .lossy {
    border-color: #e0a030;
    color: #e0a030;
  }
</style>
