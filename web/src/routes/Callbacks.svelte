<script lang="ts">
  import CallbackCard from "../lib/components/CallbackCard.svelte";

  interface Props {
    /** The callback to show; empty lists them (/judge/callbacks vs /judge/callbacks/:id). */
    id?: string;
    navigate: (to: string) => void;
  }
  let { id = "", navigate }: Props = $props();
</script>

<!-- Scaffold (M5-F2): lane M5-W2 owns this view (C262): plays the child at the spot, then fixed / not yet / worse or kept / lost. -->
<h1>Callbacks</h1>
<CallbackCard {id} {navigate} />
