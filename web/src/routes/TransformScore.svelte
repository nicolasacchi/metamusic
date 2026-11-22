<script lang="ts">
  // /transforms/score (More → Make): render a score (ABC) to audio with a soundfont, as a CPU transform (DESK-T4, C363.4).
  // Source: pasted ABC or a Take that carries a score (?track_id=). The same free estimate (400 ms debounce) and Render
  // flow as the Transform panel: transformApi.ts only. When fluidsynth or the soundfont is missing the page says so
  // plainly up front and offers no form, so nothing fails after a click.
  import { onDestroy, untrack } from "svelte";
  import { api as defaultApi } from "../api/client";
  import TransformCard from "../lib/components/TransformCard.svelte";
  import TransformEstimate from "../lib/components/TransformEstimate.svelte";
  import {
    EstimateController, RenderController, cancelTransform, createTransform, estimateTransform, getCapabilities, waitForTransform,
    type Capabilities, type EstimateState, type RenderState,
  } from "../lib/transformApi";
  import { buildScoreRequest, defaultScoreForm, scoreAvailability, scoreRefusalText, SCORE_LIMITS, type ScoreForm } from "../lib/transformScore";
  import { isUnavailable, refusalText, type Refusal } from "../lib/transformView";

  interface Props {
    /** A Take to render the score of (?track_id=). */
    trackId?: string;
    client?: typeof defaultApi;
    /** Pre-loaded capabilities (server-side rendering in unit tests). */
    initialCaps?: Capabilities | null;
  }
  let { trackId = "", client = defaultApi, initialCaps = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let caps = $state<Capabilities | null>(initialCaps);
  // svelte-ignore state_referenced_locally
  let loading = $state(initialCaps === null);
  let capsRefusal = $state<Refusal | null>(null);
  // svelte-ignore state_referenced_locally
  let form = $state<ScoreForm>(defaultScoreForm(trackId));
  let est = $state<EstimateState>({ kind: "idle" });
  let run = $state<RenderState>({ kind: "idle" });
  let cancelBusy = $state(false);

  const avail = $derived(
    capsRefusal !== null && isUnavailable(capsRefusal)
      ? { available: false as const, reason: "Transforms are not available on this server yet, so a score cannot be rendered." }
      : scoreAvailability(caps),
  );
  const built = $derived(buildScoreRequest(form));
  // A field is flagged once it holds something; an untouched required field only keeps Render disabled.
  const shown = $derived({
    ...(form.abc !== "" ? { abc: built.errors.abc } : {}),
    ...(form.trackId !== "" ? { trackId: built.errors.trackId } : {}),
    ...(form.bpm !== "" ? { bpm: built.errors.bpm } : {}),
    ...(form.tail !== "" ? { tail: built.errors.tail } : {}),
    ...(form.gain !== "" ? { gain: built.errors.gain } : {}),
  } as Record<string, string | undefined>);
  const busy = $derived(run.kind === "creating" || run.kind === "live");
  const canRender = $derived(avail.available && built.body !== null && est.kind === "ok" && !busy);

  const estimator = new EstimateController({ ask: (b) => estimateTransform(client, b), onState: (s) => (est = s) });
  const runner = new RenderController({
    create: (b, key) => createTransform(client, b, key),
    wait: (id, since, t) => waitForTransform(client, id, since, t),
    cancel: (id) => cancelTransform(client, id),
    onState: (s) => (run = s),
  });
  onDestroy(() => {
    estimator.dispose();
    runner.stop();
  });

  async function loadCaps() {
    loading = true;
    const r = await getCapabilities(client);
    loading = false;
    if (r.ok) {
      caps = r.value;
      capsRefusal = null;
    } else capsRefusal = r;
  }
  $effect(() => {
    if (initialCaps === null) untrack(() => void loadCaps());
  });
  // Only an available server is asked for an estimate.
  $effect(() => {
    const b = avail.available ? built.body : null;
    untrack(() => estimator.request(b));
  });

  function render() {
    const b = built.body;
    if (!b || !canRender) return;
    void runner.start(b);
  }
  async function cancel() {
    if (cancelBusy) return;
    cancelBusy = true;
    await runner.cancelNow();
    cancelBusy = false;
  }
  const refusalLine = $derived(run.kind === "refused" || run.kind === "unavailable" ? scoreRefusalText(run.refusal) : "");
  const set = <K extends keyof ScoreForm>(k: K, v: ScoreForm[K]) => (form[k] = v);
</script>

<h1>Render score</h1>
<p class="muted intro">Turn a score in ABC notation into a new take, played by a soundfont. It uses CPU, never money.</p>

{#if loading}
  <p class="muted" data-testid="score-loading">Loading…</p>
{:else if capsRefusal && !isUnavailable(capsRefusal)}
  <p class="err" role="alert" data-testid="score-caps-error">{refusalText(capsRefusal)}</p>
  <button type="button" onclick={() => void loadCaps()}>Try again</button>
{:else if !avail.available}
  <p class="muted" role="status" data-testid="score-unavailable">{avail.reason}</p>
  <p class="muted"><a href="/transforms">Back to transforms</a></p>
{:else}
  <section class="card" data-testid="score-form">
    <div class="seg" role="group" aria-label="Source of the score">
      <button type="button" aria-pressed={form.source === "abc"} onclick={() => set("source", "abc")} data-testid="score-source-abc">Paste ABC</button>
      <button type="button" aria-pressed={form.source === "take"} onclick={() => set("source", "take")} data-testid="score-source-take">A take with a score</button>
    </div>
    {#if form.source === "abc"}
      <label class="wide">Score (ABC notation)
        <textarea rows="10" spellcheck="false" value={form.abc} oninput={(e) => set("abc", e.currentTarget.value)} aria-invalid={shown.abc ? "true" : undefined} data-testid="score-abc"></textarea>
      </label>
      {#if shown.abc}<small class="err" role="alert">{shown.abc}</small>{/if}
    {:else}
      <label class="wide">Take id
        <input type="text" autocomplete="off" spellcheck="false" value={form.trackId} oninput={(e) => set("trackId", e.currentTarget.value)} aria-invalid={shown.trackId ? "true" : undefined} data-testid="score-track" />
      </label>
      <small class="muted">The take must carry a score; open it in the library and copy its id.</small>
      {#if shown.trackId}<small class="err" role="alert">{shown.trackId}</small>{/if}
    {/if}
    <div class="fields">
      <label>Soundfont
        <select value={form.soundfont} onchange={(e) => set("soundfont", e.currentTarget.value)} data-testid="score-soundfont">
          <option value="">First configured</option>
          {#each avail.soundfonts as n (n)}<option value={n}>{n}</option>{/each}
        </select>
      </label>
      <label>Dialect
        <select value={form.dialect} onchange={(e) => set("dialect", e.currentTarget.value as ScoreForm["dialect"])} data-testid="score-dialect">
          <option value="">Automatic</option>
          <option value="standard">Standard ABC</option>
          <option value="yue2">YuE2</option>
        </select>
      </label>
      <label>Chords
        <select value={form.chords} onchange={(e) => set("chords", e.currentTarget.value as ScoreForm["chords"])} data-testid="score-chords">
          <option value="">Notes only</option>
          <option value="block">Play chord symbols</option>
        </select>
      </label>
      <label>BPM <span class="muted">({SCORE_LIMITS.bpmMin}–{SCORE_LIMITS.bpmMax})</span>
        <input type="text" inputmode="decimal" placeholder="the score's own" value={form.bpm} oninput={(e) => set("bpm", e.currentTarget.value)} aria-invalid={shown.bpm ? "true" : undefined} data-testid="score-bpm" />
        {#if shown.bpm}<small class="err" role="alert">{shown.bpm}</small>{/if}
      </label>
      <label>Tail (s) <span class="muted">(0–{SCORE_LIMITS.tailMax})</span>
        <input type="text" inputmode="decimal" placeholder={String(SCORE_LIMITS.tailDefault)} value={form.tail} oninput={(e) => set("tail", e.currentTarget.value)} aria-invalid={shown.tail ? "true" : undefined} data-testid="score-tail" />
        {#if shown.tail}<small class="err" role="alert">{shown.tail}</small>{/if}
      </label>
      <label>Gain <span class="muted">({SCORE_LIMITS.gainMin}–{SCORE_LIMITS.gainMax})</span>
        <input type="text" inputmode="decimal" placeholder={String(SCORE_LIMITS.gainDefault)} value={form.gain} oninput={(e) => set("gain", e.currentTarget.value)} aria-invalid={shown.gain ? "true" : undefined} data-testid="score-gain" />
        {#if shown.gain}<small class="err" role="alert">{shown.gain}</small>{/if}
      </label>
      <label class="wide">Name <span class="muted">(optional)</span>
        <input type="text" maxlength="200" value={form.title} oninput={(e) => set("title", e.currentTarget.value)} data-testid="score-title" />
      </label>
    </div>

    <div class="est" aria-live="polite" data-testid="score-estimate-area">
      {#if est.kind === "loading"}
        <p class="muted">Estimating…</p>
      {:else if est.kind === "ok"}
        <TransformEstimate estimate={est.estimate} />
      {:else if est.kind === "refused" || est.kind === "unavailable"}
        <p class="err" role="alert" data-testid="score-estimate-refused">{scoreRefusalText(est.refusal)}</p>
      {:else if built.body === null}
        <p class="muted">Give a score to see the estimate.</p>
      {/if}
    </div>
    <button type="button" class="primary render" disabled={!canRender} onclick={render} data-testid="score-render">Render</button>
  </section>

  <div class="status" aria-live="polite" data-testid="score-status">
    {#if run.kind === "creating"}
      <p class="muted">Starting…</p>
    {:else if run.kind === "live" || run.kind === "finished"}
      <TransformCard transform={run.view} busy={cancelBusy} oncancel={() => void cancel()} />
    {:else if run.kind === "refused" || run.kind === "unavailable"}
      <p class="err" role="alert" data-testid="score-refused">{refusalLine}</p>
    {/if}
  </div>
  <p class="muted"><a href="/transforms">All transforms</a></p>
{/if}

<style>
  .intro,
  .err {
    overflow-wrap: anywhere;
  }
  .err {
    color: var(--bad);
  }
  .card {
    min-width: 0;
    max-width: 100%;
    box-sizing: border-box;
    display: grid;
    gap: var(--sp-3);
  }
  .seg {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .seg button {
    min-height: var(--hit);
    flex: 1 1 auto;
  }
  .seg button[aria-pressed="true"] {
    background: var(--accent);
    color: var(--accent-ink);
    border-color: var(--accent);
  }
  label {
    display: flex;
    flex-direction: column;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
    min-width: 0;
  }
  textarea,
  input,
  select {
    min-height: var(--hit);
    max-width: 100%;
    box-sizing: border-box;
    font: inherit;
  }
  textarea {
    font-family: var(--font-mono, monospace);
    min-height: 10rem;
  }
  .fields {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
    gap: var(--sp-3);
  }
  .wide {
    grid-column: 1 / -1;
  }
  .est,
  .status {
    min-width: 0;
    margin: var(--sp-3) 0;
  }
  .render {
    width: 100%;
    min-height: var(--hit);
  }
</style>
