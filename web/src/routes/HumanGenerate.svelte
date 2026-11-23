<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import ConfirmSpend from "../lib/components/ConfirmSpend.svelte";
  import EstimateView from "../lib/components/EstimateView.svelte";
  import EnginePicker from "../lib/components/EnginePicker.svelte";
  import ModeForm from "../lib/components/ModeForm.svelte";
  import ModePanel from "../lib/components/ModePanel.svelte";
  import ProviderBanner from "../lib/components/ProviderBanner.svelte";
  import BulkOption from "../lib/components/BulkOption.svelte";
  import PodEstimate from "../lib/components/PodEstimate.svelte";
  import { POD_MIN_TAKES_FLOOR, bulkFailureText, bulkOffer, statusAskedAfter } from "../lib/bulkView";
  import { can } from "../lib/me.svelte";
  import { estimateFailureText, hasListeningCheckChoice, tierBSummary } from "../lib/estimateTierB";
  import { planSeedsRefusalText } from "../lib/deskRender";
  import { capsOf, engineLabel, fetchEngines, modeUnavailable, pickerChoices, type EnginesResult } from "../lib/engines";
  import {
    ACESTEP,
    clearIntent,
    engineOf,
    engineSwitchable,
    intentTakes,
    intentToEstimate,
    isEngineMode,
    isVariantMode,
    loadIntent,
    saveIntent,
    validateIntent,
    withEngine,
    withPodBackend,
    keepPodBackend,
    withSkipTierB,
    type EngineName,
    type GenerationIntent,
  } from "../lib/genIntent";

  type Estimate = components["schemas"]["Estimate"];

  /** Router navigate, for the post-confirm hop to the (SPA) batch page only; /human/* stays a document load. */
  let { navigate }: { navigate: (to: string) => void } = $props();

  let intent = $state<GenerationIntent | null>(loadIntent(sessionStorage));
  let estimate = $state<Estimate | null>(null);
  let error = $state("");
  let busy = $state(false);
  let seq = 0;
  // The same estimate with the listening check skipped: priced only to show what the check costs (M5b W1).
  let without = $state<Estimate | null>(null);
  let withoutSeq = 0;

  /** Edit modes, templates and compose need the user's input before there is anything to price. */
  function needsForm(i: GenerationIntent): boolean {
    if (isEngineMode(i.mode)) return validateIntent(i) !== null;
    if (i.mode === "compose_new" || i.mode === "from_template") return true;
    if (i.mode === "same_seed_edit" || i.mode === "lyric_edit" || i.mode === "style_edit") return !i.patch || (!i.patch.style && !i.patch.lyrics);
    return false;
  }
  let editing = $state(false);

  /* Engines: list_engines is fetched only when it matters (an ACE-Step intent, or the user asks to change engine), so the
     yue2 flow makes no extra request and, while the operation answers 501, logs no console error. */
  let engines = $state<EnginesResult | null>(null);
  let enginesBusy = $state(false);
  async function loadEngines() {
    if (engines?.kind === "ok" || enginesBusy) return;
    enginesBusy = true;
    engines = await fetchEngines();
    enginesBusy = false;
  }
  const engineList = $derived(engines?.kind === "ok" ? engines.list : null);
  const current = $derived<EngineName>(intent ? engineOf(intent) : "yue2");
  const choices = $derived(
    engineList && intent
      ? pickerChoices(engineList, (name) => (name === "yue2" && isEngineMode(intent!.mode) ? "This action needs ACE-Step." : ""))
      : [],
  );
  const panelCaps = $derived(capsOf(engineList, ACESTEP));
  const modeOff = $derived(intent && isEngineMode(intent.mode) && engineList ? modeUnavailable(intent.mode, panelCaps) : "");
  function pickEngine(name: string) {
    if (!intent || (name !== "yue2" && name !== ACESTEP)) return;
    intent = withEngine(intent, name);
    saveIntent(sessionStorage, intent);
    if (!editing) void run(intent);
  }

  async function run(i: GenerationIntent) {
    const bad = validateIntent(i);
    error = bad ?? "";
    estimate = null;
    without = null;
    ++withoutSeq;
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
      if (r.data) {
        estimate = r.data;
        void priceWithout(i, r.data);
      } else {
        const details = r.error?.details as Record<string, unknown> | undefined;
        const why = i.mode === "plan_seeds" ? planSeedsRefusalText : estimateFailureText;
        error = (i.pod_backend ? bulkFailureText(r.error?.code ?? "", details) : null) ?? why(r.response.status, r.error?.code ?? "", r.error?.message ?? "", details);
      }
    } catch {
      if (mine === seq) error = "Network error while estimating.";
    } finally {
      if (mine === seq) busy = false;
    }
  }

  /** What the listening check costs is the difference to the same batch without it: a second estimate, never confirmed. */
  async function priceWithout(i: GenerationIntent, e: Estimate) {
    if (i.skip_tier_b || !hasListeningCheckChoice(e) || tierBSummary(e).checked === 0) return;
    const mine = withoutSeq;
    try {
      const call = intentToEstimate(withSkipTierB(i, true));
      const r = call.kind === "variant" ? null : await api.POST("/api/generations/estimate", { body: call.body });
      if (mine === withoutSeq && r?.data) without = r.data;
    } catch {
      // the cost line is optional
    }
  }
  function skipTierB(skip: boolean) {
    if (!intent) return;
    intent = withSkipTierB(intent, skip);
    saveIntent(sessionStorage, intent);
    void run(intent);
  }

  /* Bulk pods (m6-bulk C398.6): only the Owner reads the provider status, so only the Owner is offered, or told about, the pod option. */
  let status = $state<components["schemas"]["ProviderStatus"] | null>(null);
  let statusAsked = false;
  async function loadStatus() {
    if (statusAsked || !can("get_provider_status")) return;
    statusAsked = true;
    try {
      const r = await api.GET("/api/provider", {});
      if (r.data) status = r.data;
      statusAsked = statusAskedAfter(r.data);
    } catch {
      statusAsked = statusAskedAfter(null); // the option stays hidden until the next try
    }
  }
  const offer = $derived(intent ? bulkOffer(status, intentTakes(intent), can("get_provider_status")) : bulkOffer(null, 0, false));
  function bulk(on: boolean) {
    if (!intent) return;
    intent = withPodBackend(intent, on ? offer.backend : null);
    saveIntent(sessionStorage, intent);
    void run(intent);
  }

  /* svelte-ignore state_referenced_locally */
  if (intent && intentTakes(intent) >= POD_MIN_TAKES_FLOOR) void loadStatus();
  /* svelte-ignore state_referenced_locally */
  if (intent) {
    if (engineOf(intent) !== "yue2") void loadEngines();
    if (needsForm(intent)) editing = true;
    else void run(intent);
  }

  function submitted(next: GenerationIntent) {
    // "Change options" keeps the bulk pod choice while the new take count still qualifies.
    const i = keepPodBackend(intent, next, POD_MIN_TAKES_FLOOR);
    intent = i;
    saveIntent(sessionStorage, i);
    editing = false;
    if (intentTakes(i) >= POD_MIN_TAKES_FLOOR) void loadStatus();
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
  <p data-testid="no-intent">Nothing to generate yet. Start from a <a href="/library">track</a>, the <a href="/backlog">backlog</a> or the library.</p>
{:else}
  <p class="muted" data-testid="intent-summary">
    {intent.mode.replaceAll("_", " ")}
    {#if current !== "yue2"}on {engineLabel(engineList, current)}{/if}
    {#if isVariantMode(intent.mode) && intent.parent_track_id}of <a href="/tracks/{intent.parent_track_id}">{intent.parent_track_id}</a>{/if}
    {#if intent.draft_ids}· {intent.draft_ids.length} draft{intent.draft_ids.length === 1 ? "" : "s"}{/if}
    {#if intent.mode === "from_template"}· {intent.parent_track_ids?.length ?? 0} tracks{/if}
    {#if intent.mode === "render_plan"}· {intent.desk?.plan_ids?.length ?? 0} plan{intent.desk?.plan_ids?.length === 1 ? "" : "s"}{#if (intent.n_seeds ?? 1) > 1}, {intent.n_seeds} seeds each{/if}{/if}
    {#if intent.mode === "plan_seeds"}· {intent.desk?.n_seeds ?? 8} new score seeds of <a href="/tracks/{intent.parent_track_id}">{intent.parent_track_id}</a>{/if}
    {#if intent.mode === "analyze"}· listening check of {intent.track_ids?.length ?? 0} take{intent.track_ids?.length === 1 ? "" : "s"}{/if}
  </p>
  <ProviderBanner provider={estimate?.provider ?? null} />
  {#if engineSwitchable(intent)}
    {#if choices.length}
      <EnginePicker {choices} value={current} onpick={pickEngine} />
    {:else}
      <p class="engine-line" data-testid="engine-line">
        Engine: {engineLabel(engineList, current)}
        {#if engines?.kind === "unavailable"}
          <span class="muted" data-testid="engines-unavailable">· YuE2 is the only engine available.</span>
        {:else if engines?.kind === "error"}
          <span class="muted" role="alert">· {engines.message}</span>
        {:else if !isEngineMode(intent.mode)}
          <button type="button" onclick={loadEngines} disabled={enginesBusy} data-testid="change-engine">{enginesBusy ? "Loading…" : "Change engine"}</button>
        {/if}
      </p>
    {/if}
  {/if}
  {#if !editing}<BulkOption {offer} on={intent.pod_backend !== undefined} onchange={bulk} />{/if}
  {#if modeOff}<p role="alert" data-testid="mode-unavailable">{modeOff}</p>{/if}
  {#if editing && isEngineMode(intent.mode)}
    <ModePanel mode={intent.mode} {intent} caps={panelCaps} onsubmit={submitted} />
  {:else if editing}
    <ModeForm {intent} onsubmit={submitted} />
  {:else}
    {#if busy}<p class="muted" aria-busy="true">Estimating…</p>{/if}
    {#if error}<p role="alert" data-testid="estimate-error">{error}</p>{/if}
    {#if estimate}
      <EstimateView {estimate} {without} skipTierB={intent.skip_tier_b === true} onskip={skipTierB} />
      {#if estimate.pod}<PodEstimate pod={estimate.pod} backend={estimate.backend} />{/if}
      <ConfirmSpend {estimate} onbatch={opened} onreestimate={reestimate} />
    {/if}
    {#if intent.mode === "render_plan" || intent.mode === "plan_seeds"}
      <p><a href="/desk" data-testid="back-to-desk">Back to the Desk</a></p>
    {:else if intent.mode !== "analyze"}
      <p><button type="button" onclick={() => (editing = true)} data-testid="change-options">Change options</button></p>
    {/if}
  {/if}
{/if}

<style>
  .engine-line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .engine-line button {
    min-height: var(--hit);
    min-width: var(--hit);
  }
</style>
