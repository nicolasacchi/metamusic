<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import ConfirmSpend from "../lib/components/ConfirmSpend.svelte";
  import EstimateView from "../lib/components/EstimateView.svelte";
  import ModeForm from "../lib/components/ModeForm.svelte";
  import ProviderBanner from "../lib/components/ProviderBanner.svelte";
  import { clearIntent, intentToEstimate, isVariantMode, loadIntent, saveIntent, validateIntent, type GenerationIntent } from "../lib/genIntent";

  type Estimate = components["schemas"]["Estimate"];

  /** Router navigate, for the post-confirm hop to the (SPA) batch page only; /human/* stays a document load. */
  let { navigate }: { navigate: (to: string) => void } = $props();

  let intent = $state<GenerationIntent | null>(loadIntent(sessionStorage));
  let estimate = $state<Estimate | null>(null);
  let error = $state("");
  let busy = $state(false);
  let seq = 0;

  /** Edit modes, templates and compose need the user's input before there is anything to price. */
  function needsForm(i: GenerationIntent): boolean {
    if (i.mode === "compose_new" || i.mode === "from_template") return true;
    if (i.mode === "same_seed_edit" || i.mode === "lyric_edit" || i.mode === "style_edit") return !i.patch || (!i.patch.style && !i.patch.lyrics);
    return false;
  }
  let editing = $state(false);

  async function run(i: GenerationIntent) {
    const bad = validateIntent(i);
    error = bad ?? "";
    estimate = null;
    if (bad) return;
    const mine = ++seq;
    busy = true;
    try {
      const call = intentToEstimate(i);
      const r =
        call.kind === "variant"
          ? await api.POST("/api/tracks/{id}/variants/estimate", { params: { path: { id: call.trackId } }, body: call.body })
          : await api.POST("/api/generations/estimate", { body: call.body });
      if (mine !== seq) return;
      if (r.data) estimate = r.data;
      else error = r.error?.message ? `${r.error.code}: ${r.error.message}` : "Could not estimate.";
    } catch {
      if (mine === seq) error = "Network error while estimating.";
    } finally {
      if (mine === seq) busy = false;
    }
  }

  /* svelte-ignore state_referenced_locally */
  if (intent) {
    if (needsForm(intent)) editing = true;
    else void run(intent);
  }

  function submitted(i: GenerationIntent) {
    intent = i;
    saveIntent(sessionStorage, i);
    editing = false;
    void run(i);
  }
  function opened(id: string) {
    clearIntent(sessionStorage);
    navigate("/batches/" + id);
  }
  function reestimate() {
    if (intent) void run(intent);
  }
</script>

<h1>Generate</h1>
{#if !intent}
  <p data-testid="no-intent">Nothing to generate yet. Start from a <a href="/">track</a>, the <a href="/backlog">backlog</a> or the library.</p>
{:else}
  <p class="muted" data-testid="intent-summary">
    {intent.mode.replaceAll("_", " ")}
    {#if isVariantMode(intent.mode) && intent.parent_track_id}of <a href="/tracks/{intent.parent_track_id}">{intent.parent_track_id}</a>{/if}
    {#if intent.draft_ids}· {intent.draft_ids.length} draft{intent.draft_ids.length === 1 ? "" : "s"}{/if}
    {#if intent.mode === "from_template"}· {intent.parent_track_ids?.length ?? 0} tracks{/if}
  </p>
  <ProviderBanner provider={estimate?.provider ?? null} />
  {#if editing}
    <ModeForm {intent} onsubmit={submitted} />
  {:else}
    {#if busy}<p class="muted" aria-busy="true">Estimating…</p>{/if}
    {#if error}<p role="alert" data-testid="estimate-error">{error}</p>{/if}
    {#if estimate}
      <EstimateView {estimate} />
      <ConfirmSpend {estimate} onbatch={opened} onreestimate={reestimate} />
    {/if}
    <p><button type="button" onclick={() => (editing = true)} data-testid="change-options">Change options</button></p>
  {/if}
{/if}
