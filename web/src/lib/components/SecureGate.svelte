<script lang="ts">
  // Wraps every /human/* page (D1). Those pages are SPA routes now, so nothing may render from the page's own
  // (partly ungated /api) reads until a gated call has succeeded: the gate probe is a human-only read, answered by
  // the edge's two-factor rule or the local step-up (a failure runs api/stepup.ts and is retried in place), and only
  // a 200 for a human principal opens the content. Agents and devices never see these pages' data.
  // The caller comes from the `me` store (no whoami call here). Only a caller who may list devices is probed: a human
  // without that op (an Editor on Generate-as-request) has no human-only call on the page, so there is nothing to gate
  // and the probe would only be refused. The route's own op is checked by App before this renders (C313.3).
  import type { Snippet } from "svelte";
  import { api } from "../../api/client";
  import { can, me } from "../me.svelte";

  let { children }: { children: Snippet } = $props();
  let phase = $state<"checking" | "open" | "denied" | "failed">("checking");

  async function check() {
    phase = "checking";
    try {
      if (me.kind !== "human") {
        phase = me.loaded ? "denied" : "failed";
        return;
      }
      if (!can("list_devices")) {
        phase = "open";
        return;
      }
      const r = await api.GET("/human/api/devices", {});
      phase = r.response.status === 200 ? "open" : r.response.status === 403 ? "denied" : "failed";
    } catch {
      phase = "failed";
    }
  }
  $effect(() => {
    void check();
  });
</script>

{#if phase === "open"}
  {@render children()}
{:else if phase === "checking"}
  <p class="muted" role="status" data-testid="secure-checking">Checking access…</p>
{:else if phase === "denied"}
  <p role="alert" data-testid="secure-denied">This page is only for the operator's signed-in session.</p>
{:else}
  <p role="alert" data-testid="secure-failed">
    Could not confirm access. <button type="button" onclick={() => void check()}>Try again</button>
  </p>
{/if}
