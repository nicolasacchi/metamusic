<script lang="ts">
  import PatchView from "../lib/components/PatchView.svelte";

  interface Props {
    id: string;
    navigate: (to: string) => void;
  }
  let { id, navigate }: Props = $props();
</script>

<!-- Lane M5-W1 (C262): diff (lyrics, style and, from DESK-R1, the bars), scope, renders and callbacks live in PatchView; its render button goes to /human/generate. -->
<h1>Song PR</h1>
<PatchView {id} {navigate} />
