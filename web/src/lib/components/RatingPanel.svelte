<script lang="ts">
  // Other people's ratings of a take (C313.5). Renders only what the response carries: the Owner's `others` and the
  // `average` (Owner and Editor). A Listener's response has neither, so nothing is drawn. Read-only.
  import { averageLabel, ratingView, raterLabel, starsLabel, type TrackRating } from "../ratingView";

  let { rating }: { rating: Partial<TrackRating> | null | undefined } = $props();
  const v = $derived(ratingView(rating));
</script>

{#if v.average || (v.others && v.others.length > 0)}
  <div class="ratings" data-testid="rating-panel">
    {#if v.average}<span class="muted" data-testid="rating-average">average {averageLabel(v.average)}</span>{/if}
    {#if v.others && v.others.length > 0}
      <ul data-testid="rating-others">
        {#each v.others as o (o.user)}
          <li data-testid="rating-other"><strong data-former={o.former}>{raterLabel(o)}</strong> {starsLabel(o)}{o.verdict ? ` — ${o.verdict}` : ""}</li>
        {/each}
      </ul>
    {/if}
  </div>
{/if}

<style>
  .ratings ul {
    list-style: none;
    margin: 0.25rem 0 0;
    padding: 0;
  }
</style>
