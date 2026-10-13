<script lang="ts">
  // Replaces the Recipe and Lyrics panels of a reference track (M2c C64).
  interface Props {
    /** The stub recipe's title. */
    title: string;
    lossy: boolean;
    masterBytes: number;
    durationS: number;
  }
  let { title, lossy, masterBytes, durationS }: Props = $props();
  const kbps = $derived(durationS > 0 ? Math.round((masterBytes * 8) / durationS / 1000) : 0);
</script>

<section class="card" data-testid="recipe-unknown">
  <h2>Recipe unknown</h2>
  <p>No recipe exists for this track. It was imported as a reference take: it can be listened to, rated, commented and compared, but it cannot be re-rendered or used as a mother.</p>
  <p class="muted">Stub title: {title}</p>
  {#if lossy}
    <p class="muted" data-testid="lossy-info">Lossy MP3 master, {kbps} kbps. There is no lossless master.</p>
  {/if}
</section>

<style>
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
  }
  h2 {
    margin: 0 0 0.4rem;
    font-size: 1rem;
  }
  p {
    margin: 0.3rem 0;
  }
</style>
