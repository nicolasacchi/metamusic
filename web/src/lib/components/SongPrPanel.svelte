<script lang="ts">
  interface Props {
    trackId: string;
  }
  let { trackId }: Props = $props();
</script>

<!-- Scaffold (M5-F2), mounted on the Track page: lane M5-W1 fills it (C262): open fix comments with spots, the "Propose a patch"
     form with the bounds shown, the patch list with renders and callback tallies. Renders nothing until then. -->
<section data-testid="song-pr-panel" data-track={trackId} hidden></section>
