<script lang="ts">
  import PatchView from "../lib/components/PatchView.svelte";

  interface Props {
    id: string;
    navigate: (to: string) => void;
  }
  let { id, navigate }: Props = $props();
</script>

<!-- Scaffold (M5-F2): lane M5-W1 owns this page (C262): diff, scope, renders, callbacks; the render button goes to /human/generate. -->
<h1>Song PR</h1>
<PatchView {id} {navigate} />
