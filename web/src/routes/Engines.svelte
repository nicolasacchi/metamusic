<script lang="ts">
  // /engines (More): the read-only capability matrix. list_engines is fetched here only; it answers 501 until the
  // engines operation lands, which this page explains instead of failing.
  import { fetchEngines, type EngineList } from "../lib/engines";
  import EngineMatrix from "../lib/components/EngineMatrix.svelte";

  interface Props {
    /** Pre-loaded list (unit tests). */
    initial?: EngineList;
  }
  let { initial }: Props = $props();

  // svelte-ignore state_referenced_locally
  let list = $state<EngineList | null>(initial ?? null);
  let message = $state("");
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);

  $effect(() => {
    if (initial) return;
    void fetchEngines().then((r) => {
      if (r.kind === "ok") list = r.list;
      else message = r.kind === "unavailable" ? "YuE2 is the only engine available." : r.message;
      loading = false;
    });
  });
</script>

<h1>Engines</h1>
{#if loading}
  <p class="muted">Loading…</p>
{:else if list}
  <p class="muted">What each render engine can do, whether it is adopted and the state of its backend.</p>
  <EngineMatrix {list} />
{:else}
  <p class="muted" data-testid="engines-unavailable">{message}</p>
{/if}
