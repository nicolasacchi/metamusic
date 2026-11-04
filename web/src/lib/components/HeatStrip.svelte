<script lang="ts">
  interface Props {
    trackId: string;
    /** Seconds; the strip spans the take. */
    duration: number;
  }
  let { trackId, duration }: Props = $props();
</script>

<!-- Scaffold (M5-F2), mounted under the waveform of the Track page: lane M5-W1 draws the 120 bins and the hotspot chips
     (jump with jumpTo) from get_listen_heat (C256, C262). Renders nothing until then. -->
<div data-testid="heat-strip" data-track={trackId} data-duration={duration} hidden></div>
