<script lang="ts">
  import { onMount } from "svelte";
  import { routeReady } from "../lib/scrollRestore";
  let { id }: { id: string } = $props();
  onMount(() => void routeReady());
</script>

<section data-testid="crate-page" data-id={id}>
  <h1>Crate</h1>
  <p>Not built yet.</p>
</section>
