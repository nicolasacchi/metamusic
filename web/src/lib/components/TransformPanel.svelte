<script lang="ts">
  /** The Transform panel of the Take page (T1). The forms, estimate and render controls arrive with lane W1; F1 only reserves the place. */
  let { trackId }: { trackId: string } = $props();
</script>

<section class="card" data-testid="transform-panel" data-track-id={trackId}>
  <h2>Transform</h2>
  <p class="muted">Make a new take from this one: stretch it, stack it, polish it. Coming with the transform controls.</p>
</section>
