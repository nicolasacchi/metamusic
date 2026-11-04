<script lang="ts">
  // The Transform panel of the Take page (T1 C214.2): a segmented control over the kinds the server lists, a form generated
  // from the capabilities table, a debounced (400 ms) free estimate, Render (create, then follow with progress and Cancel),
  // and a link to the new Take. Phone width: the form lives in a bottom sheet above the player; progress and Cancel stay
  // on the card. Everything goes through web/src/api/ (transformApi.ts); 501/503 show "not available yet".
  import { onDestroy, untrack } from "svelte";
  import { api as defaultApi } from "../../api/client";
  import { pushNavigate, openAbWithParent } from "../abParent";
  import {
    EstimateController, RenderController, cancelTransform, createTransform, estimateTransform, getCapabilities, listTransforms,
    sameLyricsTakes, waitForTransform, type Capabilities, type EstimateState, type PartnerTake, type RenderState,
  } from "../transformApi";
  import { buildRequest, defaultForm, fieldsFor, kindOffered, type FormState } from "../transformForm";
  import { explainRateLimit, isLive, isUnavailable, kindLabel, orderKinds, refusalText, sortTransforms, type Refusal, type TransformView } from "../transformView";
  import { viewport } from "../viewport.svelte";
  import TransformCard from "./TransformCard.svelte";
  import TransformEstimate from "./TransformEstimate.svelte";
  import TransformForm from "./TransformForm.svelte";

  interface Props {
    trackId: string;
    /** The Take carries score.abc (offers "Render score" when the server lists it). */
    hasScore?: boolean;
    client?: typeof defaultApi;
    /** Pre-loaded capabilities (server-side rendering in unit tests). */
    initialCaps?: Capabilities | null;
  }
  let { trackId, hasScore = false, client = defaultApi, initialCaps = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let caps = $state<Capabilities | null>(initialCaps);
  // svelte-ignore state_referenced_locally
  let capsLoading = $state(initialCaps === null);
  let capsRefusal = $state<Refusal | null>(null);
  let kind = $state("");
  let form = $state<FormState>(defaultForm(""));
  let est = $state<EstimateState>({ kind: "idle" });
  let run = $state<RenderState>({ kind: "idle" });
  let recent = $state<TransformView[]>([]);
  let partners = $state<PartnerTake[]>([]);
  let partnersLoading = $state(false);
  let partnersFor = "";
  let abError = $state("");
  let abBusy = $state(false);
  let cancelBusy = $state(false);
  let sheetOpen = $state(false);
  let resumed = false;

  const ctx = $derived({ trackId, hasScore });
  const kinds = $derived(caps ? orderKinds(caps.kinds.map((k) => k.kind)).filter((k) => kindOffered(k, caps?.kinds.map((x) => x.kind) ?? [], ctx)) : []);
  // The chosen kind, else the first one offered (so the first form renders without a click, also server-side).
  const activeKind = $derived(kind && kinds.includes(kind) ? kind : (kinds[0] ?? ""));
  const kindCaps = $derived(caps?.kinds.find((k) => k.kind === activeKind) ?? null);
  const fields = $derived(kindCaps ? fieldsFor(kindCaps, ctx) : []);
  const built = $derived(kindCaps ? buildRequest(kindCaps, form, ctx) : { body: null, errors: {} });
  // A field is flagged once it holds something; an untouched required field only keeps Render disabled.
  const shown = $derived(Object.fromEntries(Object.entries(built.errors).filter(([k]) => (form.values[k] ?? "") !== "")));
  const busy = $derived(run.kind === "creating" || run.kind === "live");
  const unavailable = $derived((capsRefusal !== null && isUnavailable(capsRefusal)) || (caps !== null && !caps.enabled));
  const canRender = $derived(built.body !== null && est.kind === "ok" && !busy);

  const estimator = new EstimateController({
    ask: (b) => estimateTransform(client, b),
    onState: (s) => (est = s),
  });
  const runner = new RenderController({
    create: (b, key) => createTransform(client, b, key),
    wait: (id, since, t) => waitForTransform(client, id, since, t),
    cancel: (id) => cancelTransform(client, id),
    onState: (s) => {
      run = s;
      if (s.kind === "finished") void loadRecent();
    },
  });
  onDestroy(() => {
    estimator.dispose();
    runner.stop();
  });

  async function loadCaps() {
    capsLoading = true;
    const r = await getCapabilities(client);
    capsLoading = false;
    if (r.ok) {
      caps = r.value;
      capsRefusal = null;
    } else capsRefusal = r;
  }
  async function loadRecent() {
    const r = await listTransforms(client, { track_id: trackId, limit: 5 });
    if (!r.ok) return;
    recent = sortTransforms(r.value.items);
    if (!resumed) {
      resumed = true;
      const live = recent.find((t) => isLive(t.state));
      if (live && run.kind === "idle") void runner.follow(live);
    }
  }
  async function loadPartners() {
    partnersLoading = true;
    const mine = trackId;
    const r = await sameLyricsTakes(client, mine);
    if (mine !== trackId) return;
    partnersLoading = false;
    partners = r.ok ? r.value : [];
  }

  function selectKind(k: string) {
    if (k === activeKind) return;
    form = { ...defaultForm(k), title: form.title };
    kind = k;
    run = run.kind === "refused" || run.kind === "unavailable" ? { kind: "idle" } : run;
  }

  $effect(() => {
    if (initialCaps === null) untrack(() => void loadCaps());
  });
  $effect(() => {
    if (caps && caps.enabled) untrack(() => void loadRecent());
  });
  $effect(() => {
    const b = built.body;
    untrack(() => estimator.request(b));
  });
  $effect(() => {
    const needs = fields.some((f) => f.key === "track_b_id");
    if (needs && partnersFor !== trackId) {
      partnersFor = trackId;
      untrack(() => void loadPartners());
    }
  });

  function render() {
    const b = built.body;
    if (!b || !canRender) return;
    sheetOpen = false;
    abError = "";
    void runner.start(b);
  }
  async function cancel() {
    if (cancelBusy) return;
    cancelBusy = true;
    await runner.cancelNow();
    cancelBusy = false;
  }
  async function cancelRecent(id: string) {
    if (cancelBusy) return;
    cancelBusy = true;
    await cancelTransform(client, id);
    cancelBusy = false;
    void loadRecent();
  }
  async function ab(id: string) {
    if (abBusy) return;
    abBusy = true;
    abError = (await openAbWithParent(trackId, id, pushNavigate)) || "";
    abBusy = false;
  }
  function key(e: KeyboardEvent) {
    if (e.key === "Escape" && sheetOpen) sheetOpen = false;
  }

  const refusalLine = $derived.by(() => {
    if (run.kind !== "refused" && run.kind !== "unavailable") return "";
    const lastEst = est.kind === "ok" ? est.estimate : null;
    return refusalText(explainRateLimit(run.refusal, lastEst, caps?.queue.max ?? null));
  });
  const otherRecent = $derived(recent.filter((t) => !((run.kind === "live" || run.kind === "finished") && run.view.id === t.id)));
</script>

<svelte:window onkeydown={key} />

{#snippet formBody()}
  <div class="kinds" role="group" aria-label="Kind of transform" data-testid="transform-kinds">
    {#each kinds as k (k)}
      <button type="button" class="kind" aria-pressed={activeKind === k} onclick={() => selectKind(k)} data-testid="transform-kind-{k}">{kindLabel(k)}</button>
    {/each}
  </div>
  {#if kindCaps}
    <p class="muted summary" data-testid="transform-summary">{kindCaps.summary}</p>
    {#if kindCaps.listening === "headphones"}<p class="note" data-testid="transform-headphones">Headphones only: this places the sound in front of the head.</p>{/if}
    {#if kindCaps.blind_spots.length}
      <details class="blind">
        <summary>What this cannot do</summary>
        <ul>{#each kindCaps.blind_spots as b, i (i)}<li>{b}</li>{/each}</ul>
        {#if kindCaps.dataset}<p class="muted">{kindCaps.dataset}</p>{/if}
      </details>
    {/if}
    <TransformForm {fields} bind:form errors={shown} {partners} {partnersLoading} />
    <div class="est" aria-live="polite" data-testid="transform-estimate-area">
      {#if est.kind === "loading"}
        <p class="muted">Estimating…</p>
      {:else if est.kind === "ok"}
        <TransformEstimate estimate={est.estimate} />
      {:else if est.kind === "refused" || est.kind === "unavailable"}
        <p class="err" role="alert" data-testid="transform-estimate-refused">{refusalText(est.refusal)}</p>
      {:else if built.body === null}
        <p class="muted">Fill in the required settings to see the estimate.</p>
      {/if}
    </div>
    <button type="button" class="primary render" disabled={!canRender} onclick={render} data-testid="transform-render">Render</button>
  {/if}
{/snippet}

<section class="card panel" data-testid="transform-panel" data-track-id={trackId}>
  <h2>Transform</h2>
  {#if capsLoading}
    <p class="muted" data-testid="transform-loading">Loading…</p>
  {:else if unavailable}
    <p class="muted" data-testid="transform-unavailable">Transforms are not available on this server yet.</p>
  {:else if capsRefusal}
    <p class="err" role="alert" data-testid="transform-caps-error">{refusalText(capsRefusal)}</p>
    <button type="button" onclick={() => void loadCaps()}>Try again</button>
  {:else if caps}
    <p class="muted intro">Make a new take from this one: stretch it, stack it, polish it.</p>
    {#if viewport.phone}
      <button type="button" class="primary open" onclick={() => (sheetOpen = true)} disabled={busy} data-testid="transform-open-sheet">Transform this take…</button>
      {#if sheetOpen}
        <div class="sheet" role="dialog" aria-modal="false" aria-label="Transform this take" data-testid="transform-sheet">
          <div class="sheet-head">
            <b>Transform</b>
            <button type="button" class="close" onclick={() => (sheetOpen = false)} aria-label="Close" data-testid="transform-sheet-close">Close</button>
          </div>
          <div class="sheet-body">{@render formBody()}</div>
        </div>
      {/if}
    {:else}
      {@render formBody()}
    {/if}

    <div class="status" aria-live="polite" data-testid="transform-status">
      {#if run.kind === "creating"}
        <p class="muted">Starting…</p>
      {:else if run.kind === "live" || run.kind === "finished"}
        <TransformCard transform={run.view} busy={cancelBusy || abBusy} oncancel={() => void cancel()} onab={(id) => void ab(id)} />
      {:else if run.kind === "refused" || run.kind === "unavailable"}
        <p class="err" role="alert" data-testid="transform-refused">{refusalLine}</p>
      {/if}
      {#if abError}<p class="err" role="alert">{abError}</p>{/if}
    </div>

    {#if otherRecent.length}
      <h3 class="recent-h">Transforms of this take</h3>
      <div class="recent" data-testid="transform-recent">
        {#each otherRecent as t (t.id)}<TransformCard transform={t} compact busy={cancelBusy} oncancel={(id) => void cancelRecent(id)} />{/each}
      </div>
    {/if}
  {/if}
</section>

<style>
  .panel {
    min-width: 0;
    max-width: 100%;
    box-sizing: border-box;
  }
  .kinds {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    margin-bottom: var(--sp-3);
  }
  .kind {
    min-height: var(--hit);
    flex: 1 1 auto;
  }
  .kind[aria-pressed="true"] {
    background: var(--accent);
    color: var(--accent-ink);
    border-color: var(--accent);
  }
  .summary,
  .note,
  .intro {
    overflow-wrap: anywhere;
  }
  .blind {
    margin: var(--sp-2) 0;
    overflow-wrap: anywhere;
  }
  .blind summary {
    min-height: var(--hit);
    display: flex;
    align-items: center;
    cursor: pointer;
  }
  .est {
    margin: var(--sp-3) 0;
    min-width: 0;
  }
  .err {
    color: var(--bad);
    overflow-wrap: anywhere;
  }
  .render,
  .open {
    width: 100%;
    min-height: var(--hit);
  }
  .status {
    margin-top: var(--sp-3);
    min-width: 0;
  }
  .recent {
    display: grid;
    gap: var(--sp-2);
    min-width: 0;
  }
  .sheet {
    position: fixed;
    left: 0;
    right: 0;
    bottom: calc(var(--tabbar-h) + var(--safe-b) + var(--player-h, 0px));
    max-height: 65dvh;
    box-sizing: border-box;
    display: flex;
    flex-direction: column;
    background: var(--bg-raised);
    border-top: 1px solid var(--line-strong);
    border-radius: var(--r-lg) var(--r-lg) 0 0;
    z-index: var(--z-sheet);
    overflow: hidden;
  }
  .sheet-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--sp-2) var(--sp-4);
    border-bottom: 1px solid var(--line-strong);
  }
  .close {
    min-height: var(--hit);
  }
  .sheet-body {
    overflow-y: auto;
    overflow-x: hidden;
    padding: var(--sp-3) var(--sp-4) var(--sp-4);
    min-width: 0;
  }
</style>
