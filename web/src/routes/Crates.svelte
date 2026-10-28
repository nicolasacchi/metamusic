<script lang="ts">
  import { onMount } from "svelte";
  import { routeReady } from "../lib/scrollRestore";
  onMount(() => void routeReady());
</script>

<section data-testid="crates-page">
  <h1>Crates</h1>
  <p>Not built yet.</p>
</section>
