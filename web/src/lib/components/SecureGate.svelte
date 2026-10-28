<script lang="ts">
  // Wraps every /human/* page (D1). Those pages are SPA routes now, so nothing may render from the page's own
  // (partly ungated /api) reads until a gated call has succeeded: the gate probe is a human-only read, answered by
  // the edge's two-factor rule or the local step-up (a failure runs api/stepup.ts and is retried in place), and only
  // a 200 for a human principal opens the content. Agents and devices never see these pages' data.
  import type { Snippet } from "svelte";
  import { api } from "../../api/client";

  let { children }: { children: Snippet } = $props();
  let phase = $state<"checking" | "open" | "denied" | "failed">("checking");

  // One probe. "failed" is a transient fault (network error, 5xx, an unanswered whoami); "denied" is an answer.
  async function probe(): Promise<"open" | "denied" | "failed"> {
    try {
      const me = await api.GET("/api/me", {});
      if (me.data?.kind !== "human") return me.data ? "denied" : "failed";
      const r = await api.GET("/human/api/devices", {});
      return r.response.status === 200 ? "open" : r.response.status === 403 ? "denied" : "failed";
    } catch {
      return "failed";
    }
  }

  // A transient fault is retried in place a few times before the page asks the operator to tap "Try again": the hub
  // is opened to act on what is waiting, and one dropped request must not leave it on an error.
  const RETRY_DELAYS_MS = [300, 900, 2000];
  let run = 0;
  async function check() {
    const mine = ++run;
    phase = "checking";
    for (let i = 0; ; i++) {
      const r = await probe();
      if (mine !== run) return;
      const delay = RETRY_DELAYS_MS[i];
      if (r !== "failed" || delay === undefined) {
        phase = r;
        return;
      }
      await new Promise((ok) => setTimeout(ok, delay));
      if (mine !== run) return;
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
