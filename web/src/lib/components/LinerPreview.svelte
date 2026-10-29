<script lang="ts">
  import type { components } from "../../api/schema";

  interface Props {
    liner: components["schemas"]["RecordLiner"] | null | undefined;
  }
  let { liner }: Props = $props();
  let explain = $state(false);
</script>

<section aria-label="Liner notes" data-testid="record-liner">
  {#if !liner}
    <p class="muted">No liner notes yet.</p>
  {:else}
    <pre>{liner.markdown}</pre>
    <button type="button" aria-pressed={explain} onclick={() => (explain = !explain)} data-testid="liner-export-toggle">What an export would say</button>
    {#if explain}
      <p class="muted" data-testid="liner-export-note">
        This preview is for the app and may mention the inspirations behind a song. A tape export uses the export version: it never names an inspiration unless that opt-in is on
        in Curation settings, and it adds the track list with offsets.
      </p>
    {/if}
  {/if}
</section>

<style>
  pre {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-family: inherit;
    background: var(--bg-sunken);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    padding: var(--sp-3);
    margin: 0 0 var(--sp-3);
  }
</style>
