<script lang="ts">
  import { pickEvidenceText } from "../pickView";

  interface Props {
    /** none | pinned | ladder | stars | newest */
    source: string | null | undefined;
    evidence?: unknown;
  }
  let { source, evidence }: Props = $props();
  const text = $derived(evidence === undefined ? "" : pickEvidenceText(evidence));
  const label = $derived(source === "pinned" ? "Pinned" : source === "ladder" ? "Duels" : source === "stars" ? "Stars" : source === "newest" ? "Newest" : "");
</script>

{#if source && source !== "none"}
  <span class="pick" data-testid="pick-badge" title={text || undefined}>★ Pick{#if label}<span class="src"> · {label}</span>{/if}{#if text}<span class="ev"> · {text}</span>{/if}</span>
{/if}

<style>
  .pick {
    display: inline-block;
    padding: 0 0.45rem;
    border-radius: 0.6rem;
    background: #f2a65a;
    color: #14110f;
    font-size: 0.8rem;
    white-space: nowrap;
  }
  .src,
  .ev {
    font-weight: 400;
  }
</style>
