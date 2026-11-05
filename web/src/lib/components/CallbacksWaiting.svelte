<script lang="ts">
  // The Judge landing's "callbacks waiting" count. Hidden while loading, when none wait, and when the op is not available.
  import { api } from "../../api/client";
  import { loadWaiting, waitingText, type Waiting } from "../callbackView";

  let w = $state<Waiting | null>(null);
  $effect(() => {
    let live = true;
    void loadWaiting(api).then((r) => {
      if (live) w = r;
    });
    return () => {
      live = false;
    };
  });
</script>

{#if w && w.kind === "count" && w.n > 0}
  <a class="waiting" href="/judge/callbacks" data-testid="callbacks-waiting">{waitingText(w.n)}</a>
{/if}

<style>
  .waiting {
    display: flex;
    align-items: center;
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    margin-bottom: var(--sp-2);
    border: 1px solid var(--accent);
    border-radius: var(--r-md);
    background: var(--accent-soft);
    color: var(--accent);
    font-weight: var(--fw-bold);
    text-decoration: none;
  }
</style>
