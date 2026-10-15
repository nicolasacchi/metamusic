<script lang="ts">
  import { tempoText } from "../songView";

  interface Props {
    tempo: { requested_bpm?: number | null; measured_bpm?: number | null; mismatch?: boolean; octave?: boolean } | null | undefined;
  }
  let { tempo }: Props = $props();
  const text = $derived(tempoText(tempo));
</script>

{#if text}
  <span class="tempo-badge" class:warn={tempo?.mismatch && !tempo?.octave} data-testid="tempo-badge">{text}</span>
{/if}

<style>
  .tempo-badge {
    display: inline-block;
    padding: 0 0.45rem;
    border: 1px solid #5a4d43;
    border-radius: 0.6rem;
    font-size: 0.8rem;
    white-space: nowrap;
  }
  .warn {
    border-color: #d9822b;
  }
</style>
