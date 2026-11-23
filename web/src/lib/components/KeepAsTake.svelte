<script lang="ts">
  // "Keep as Take" (DESK-W2, m5b-desk C363.2): an explicit score_render transform of this plan's score, so the soundfont render
  // becomes a Take in the library. CPU only, never money; it is not a GPU render (that is Render). Offered only when the server
  // can render scores, and says so plainly when it cannot.
  import { onDestroy } from "svelte";
  import { api as defaultApi } from "../../api/client";
  import TransformCard from "./TransformCard.svelte";
  import { keepRequest } from "../deskRender";
  import type { Plan } from "../deskApi";
  import { RenderController, cancelTransform, createTransform, getCapabilities, waitForTransform, type Capabilities, type RenderState } from "../transformApi";
  import { scoreAvailability, scoreRefusalText } from "../transformScore";

  interface Props {
    plan: Pick<Plan, "abc" | "title" | "base" | "score">;
    client?: typeof defaultApi;
    initialCaps?: Capabilities | null;
  }
  let { plan, client = defaultApi, initialCaps = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let caps = $state<Capabilities | null>(initialCaps);
  // svelte-ignore state_referenced_locally
  let loaded = $state(initialCaps !== null);
  let run = $state<RenderState>({ kind: "idle" });
  let cancelBusy = $state(false);

  const avail = $derived(scoreAvailability(caps));
  const body = $derived(keepRequest(plan));
  const busy = $derived(run.kind === "creating" || run.kind === "live");

  const runner = new RenderController({
    create: (b, key) => createTransform(client, b, key),
    wait: (id, since, t) => waitForTransform(client, id, since, t),
    cancel: (id) => cancelTransform(client, id),
    onState: (s) => (run = s),
  });
  onDestroy(() => runner.stop());

  $effect(() => {
    if (initialCaps !== null) return;
    let live = true;
    void getCapabilities(client).then((r) => {
      if (!live) return;
      caps = r.ok ? r.value : null;
      loaded = true;
    });
    return () => {
      live = false;
    };
  });
  const start = () => {
    if (body && !busy) void runner.start(body);
  };
  async function cancel() {
    if (cancelBusy) return;
    cancelBusy = true;
    await runner.cancelNow();
    cancelBusy = false;
  }
  const refusal = $derived(run.kind === "refused" || run.kind === "unavailable" ? scoreRefusalText(run.refusal) : "");
</script>

<section class="card" aria-labelledby="keep-h" data-testid="keep-panel">
  <h2 id="keep-h">Keep as Take</h2>
  {#if !loaded}
    <p class="muted">Loading…</p>
  {:else if !avail.available}
    <p class="muted" role="status" data-testid="keep-unavailable">{avail.reason}</p>
  {:else}
    <p class="muted">Save the soundfont version of this score as a Take in the library. It uses CPU, never money.</p>
    <button type="button" disabled={busy || body === null} onclick={start} data-testid="keep-make">{busy ? "Rendering…" : "Keep as Take"}</button>
    <div aria-live="polite" data-testid="keep-status">
      {#if run.kind === "live" || run.kind === "finished"}
        <TransformCard transform={run.view} busy={cancelBusy} oncancel={() => void cancel()} />
      {:else if refusal}
        <p class="err" role="alert" data-testid="keep-refused">{refusal}</p>
      {/if}
    </div>
  {/if}
</section>

<style>
  button {
    min-height: var(--hit);
  }
  .err {
    color: var(--bad);
    overflow-wrap: anywhere;
  }
</style>
