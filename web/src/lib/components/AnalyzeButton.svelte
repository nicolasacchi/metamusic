<script lang="ts">
  // "Analyze" (M5b C347.1): a listening check of this take. It never creates a job: it hands an analyze intent to the generate page,
  // which estimates it and asks for confirmation (the Owner spends, an Editor requests approval), like any generation.
  // With the worker image closed for it, the button is disabled and says why, instead of failing after a click.
  import { api } from "../../api/client";
  import { analyzeIntent, startGeneration } from "../genIntent";
  import { me, can } from "../me.svelte";
  import { analyzeGate } from "../tierbView";

  interface Props {
    trackId: string;
    /** Client-router navigation to /human/generate (the page reads the intent from sessionStorage). */
    go: (to: string) => void;
  }
  let { trackId, go }: Props = $props();

  // The open worker capabilities are the Owner's to read (get_provider_status); anyone else learns it from the estimate, in words.
  let caps = $state<string[] | null>(null);
  let loading = $state(false);

  $effect(() => {
    if (!can("get_provider_status")) {
      caps = null;
      return;
    }
    let live = true;
    loading = true;
    void (async () => {
      try {
        const r = await api.GET("/api/provider", {});
        if (live && r.data) caps = r.data.worker.caps;
      } catch {
        // unknown: the estimate decides
      } finally {
        if (live) loading = false;
      }
    })();
    return () => {
      live = false;
    };
  });

  const gate = $derived(analyzeGate(me.allowed, caps));
  const closed = $derived(gate.kind === "closed" ? gate.reason : "");
</script>

{#if gate.kind !== "hidden"}
  <div class="analyze" data-testid="analyze">
    <button type="button" disabled={gate.kind === "closed" || loading} aria-describedby={closed ? "analyze-why-" + trackId : undefined} onclick={() => startGeneration(sessionStorage, analyzeIntent([trackId]), go)} data-testid="analyze-button">
      Analyze
    </button>
    {#if closed}
      <p class="muted why" id="analyze-why-{trackId}" role="status" data-testid="analyze-unavailable">{closed}</p>
    {:else}
      <p class="muted why">You see the price and confirm before anything is spent.</p>
    {/if}
  </div>
{/if}

<style>
  .analyze {
    margin-top: 0.5rem;
  }
  .why {
    margin: 0.3rem 0 0;
    font-size: 0.85rem;
    overflow-wrap: anywhere;
  }
  button {
    min-height: 2rem;
  }
  @media (pointer: coarse) {
    button {
      min-height: var(--hit);
    }
  }
</style>
