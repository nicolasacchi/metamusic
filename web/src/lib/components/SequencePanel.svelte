<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { suggestSequence, type Result } from "../curationApi";
  import {
    DEFAULT_STRATEGY, STALE_TEXT, STRATEGIES, costBar, coverageText, curveModel, endpointOptions, endpointsConflict, isNoChange, pinnedRows, runText, sequenceErrorText,
    sequenceSpec, stepRows, strategyHint, strategyLabel, warningRows, type Spec, type Step, type Suggestion, type Warning,
  } from "../sequenceView";

  type View = components["schemas"]["RecordView"];
  interface Props {
    view: View;
    editable: boolean;
    busy?: boolean;
    /** From `?sequence=<strategy>`: pre-selects the strategy and runs the preview once. */
    initialStrategy?: string | null;
    /** Apply one `sequence` op; true when it was accepted. */
    onapply: (spec: Spec) => Promise<boolean>;
    /** Propose with a sequence in the same request; true when it was accepted. */
    onpropose: (rationale: string, spec: Spec) => Promise<boolean>;
    /** Reload the Record (the stale badge's refresh). */
    onreload: () => void | Promise<void>;
  }
  let { view, editable, busy = false, initialStrategy = null, onapply, onpropose, onreload }: Props = $props();

  // svelte-ignore state_referenced_locally
  let strategy = $state<string>(initialStrategy ?? DEFAULT_STRATEGY);
  let first = $state("");
  let last = $state("");
  let suggestion = $state<Suggestion | null>(null);
  /** The revision the preview was made against; a later revision makes it out of date. */
  let previewRev = $state(0);
  let previewSpec = $state<Spec>({});
  let loading = $state(false);
  let message = $state("");
  let proposing = $state(false);
  let rationale = $state("");

  const slots = $derived(view.slots);
  const tooSmall = $derived(slots.length < 2);
  const options = $derived(endpointOptions(view));
  const conflict = $derived(endpointsConflict(first, last));
  const titles = $derived(new Map(options.map((o) => [o.id, o.title])));
  const pinned = $derived(pinnedRows(view));
  const outdated = $derived(suggestion !== null && previewRev !== view.record.revision);
  const canApply = $derived(editable && !busy && !tooSmall && !conflict);
  const canPropose = $derived(editable && view.record.status === "draft" && !tooSmall && !conflict);
  const explanation = $derived(view.sequence ?? null);

  const spec = $derived(sequenceSpec(strategy, first, last));

  function fail(r: Result<unknown>): string {
    if (r.kind === "failed") return sequenceErrorText(r.code) ?? (r.message || "The preview failed");
    if (r.kind === "unavailable") return "Sequencing is not available yet.";
    if (r.kind === "stale") return r.message;
    return "Could not reach the server";
  }

  async function preview() {
    if (loading || tooSmall || conflict) return;
    loading = true;
    message = "";
    const asked = $state.snapshot(spec);
    const rev = view.record.revision;
    const r = await suggestSequence(api, view.record.id, asked);
    loading = false;
    if (r.kind === "ok") {
      suggestion = r.value;
      previewRev = rev;
      previewSpec = asked;
    } else {
      suggestion = null;
      message = fail(r);
    }
  }

  let ran = false;
  $effect(() => {
    if (ran || !initialStrategy || tooSmall) return;
    ran = true;
    void preview();
  });

  async function apply() {
    if (!canApply) return;
    message = "";
    if (await onapply(previewSpec.strategy ? previewSpec : spec)) suggestion = null;
  }

  async function propose() {
    if (!canPropose || !rationale.trim()) return;
    if (await onpropose(rationale.trim(), previewSpec.strategy ? previewSpec : spec)) {
      proposing = false;
      rationale = "";
      suggestion = null;
    }
  }

  async function refresh() {
    await onreload();
    if (suggestion || initialStrategy) await preview();
  }

  const stepsOf = (s: readonly Step[]) => stepRows(s);
  const warns = (w: readonly Warning[]) => warningRows(w, titles);
  const curve = $derived(suggestion ? curveModel(suggestion.steps) : null);
  const runCurve = $derived(explanation ? curveModel(explanation.steps) : null);
</script>

<section aria-label="Sequence" data-testid="sequence-panel">
  {#if explanation?.stale}
    <p class="stale" role="status" data-testid="sequence-stale">
      <span>{STALE_TEXT} the last run: the order or the picked takes differ now.</span>
      <button type="button" onclick={() => void refresh()}>Refresh</button>
    </p>
  {/if}

  {#if tooSmall}
    <p class="muted" data-testid="sequence-too-small">A sequence needs at least two songs. Add more on the Slots tab.</p>
  {/if}

  <form class="form" onsubmit={(e) => { e.preventDefault(); void preview(); }}>
    <label>Strategy
      <select bind:value={strategy} data-testid="sequence-strategy" disabled={tooSmall}>
        {#each STRATEGIES as s (s)}<option value={s}>{strategyLabel(s)}</option>{/each}
      </select>
    </label>
    <p class="muted hint">{strategyHint(strategy)}</p>
    <label>Start with
      <select bind:value={first} data-testid="sequence-first" disabled={tooSmall}>
        <option value="">Any song</option>
        {#each options as o (o.id)}<option value={o.id}>{o.title}</option>{/each}
      </select>
    </label>
    <label>End with
      <select bind:value={last} data-testid="sequence-last" disabled={tooSmall}>
        <option value="">Any song</option>
        {#each options as o (o.id)}<option value={o.id}>{o.title}</option>{/each}
      </select>
    </label>
    {#if conflict}<p class="err" role="alert">{sequenceErrorText("SEQUENCE_ENDPOINTS_CONFLICT")}</p>{/if}
    <button type="submit" class="primary" disabled={loading || tooSmall || conflict} data-testid="sequence-run">{loading ? "Working…" : suggestion ? "Preview again" : "Preview"}</button>
  </form>

  {#if message}<p class="err" role="alert" data-testid="sequence-error">{message}</p>{/if}

  {#if pinned.length > 0}
    <div class="pinned" data-testid="sequence-pinned">
      <h3>Pinned slots</h3>
      <p class="muted">These keep their take and side. Sequencing only changes where the slots sit.</p>
      <ul>
        {#each pinned as p (p.slotId)}
          <li>#{p.position} {p.title}{#if p.takeTitle} · take {p.takeTitle}{/if}{#if p.side} · side {p.side}{/if}</li>
        {/each}
      </ul>
    </div>
  {/if}

  {#if suggestion && curve}
    <div class="preview" data-testid="sequence-preview" data-strategy={suggestion.strategy}>
      <h3>Suggested order · {strategyLabel(suggestion.strategy)}</h3>
      {#if outdated}
        <p class="stale" role="status" data-testid="sequence-outdated">The record changed after this preview. <button type="button" onclick={() => void preview()}>Refresh preview</button></p>
      {/if}
      {#if isNoChange(suggestion)}<p class="muted" data-testid="sequence-nochange">The record is already in this order.</p>{/if}
      <p class="chips">
        <span class="chip" data-testid="sequence-coverage">{coverageText(suggestion.coverage, suggestion.steps.length, suggestion.warnings)}</span>
        {#if suggestion.exact}<span class="chip">best possible order</span>{/if}
      </p>
      {#each [costBar(suggestion.cost)] as c (c.text)}
        <div class="cost" data-testid="sequence-cost">
          <div class="bar" role="img" aria-label={c.text}><span style="width: {c.pct ?? 0}%"></span></div>
          <p class="muted">{c.text}</p>
        </div>
      {/each}
      {#if suggestion.warnings.length > 0}
        <ul class="warnings" data-testid="sequence-warnings">
          {#each warns(suggestion.warnings) as w, i (i)}<li>{w}</li>{/each}
        </ul>
      {/if}
      <ol class="steps">
        {#each stepsOf(suggestion.steps) as r (r.position)}
          <li data-testid="sequence-step">
            <span class="pos num">{r.position}</span>
            <div class="body">
              <a class="tap" href="/songs/{r.songId}">{r.title}</a>
              {#if r.takeTitle}<span class="muted"> · {r.takeTitle}</span>{/if}
              <p class="text">{r.text}</p>
              {#if r.missingText}<span class="chip warn">{r.missingText}</span>{/if}
              {#if r.energyPct !== null}<span class="ebar" aria-hidden="true"><span style="width: {r.energyPct}%"></span></span>{/if}
            </div>
          </li>
        {/each}
      </ol>
      <svg viewBox="0 0 {curve.width} {curve.height}" role="img" aria-label="Energy along the suggested order" data-testid="sequence-curve">
        {#if curve.target}<path d={curve.target} class="target" fill="none" />{/if}
        <path d={curve.line} class="line" fill="none" />
        {#each curve.dots as d, i (i)}{#if d.known}<circle cx={d.x} cy={d.y} r="3" />{/if}{/each}
      </svg>
      <div class="actions">
        <button type="button" class="primary" disabled={!canApply || outdated || isNoChange(suggestion)} onclick={() => void apply()} data-testid="sequence-apply">Apply</button>
        {#if canPropose}
          <button type="button" disabled={busy || outdated} aria-expanded={proposing} onclick={() => (proposing = !proposing)} data-testid="sequence-propose">Propose with this order</button>
        {/if}
      </div>
      {#if !editable}<p class="muted">You can look at the preview but not change this record.</p>{/if}
      {#if proposing && canPropose}
        <form class="form" onsubmit={(e) => { e.preventDefault(); void propose(); }}>
          <label>Why is it ready? <textarea bind:value={rationale} rows="3" maxlength="500" required data-testid="sequence-rationale"></textarea></label>
          <button type="submit" class="primary" disabled={busy || !rationale.trim()} data-testid="sequence-send">Send proposal</button>
        </form>
      {/if}
    </div>
  {/if}

  {#if explanation && runCurve}
    <div class="run" data-testid="sequence-run-info">
      <h3>Latest run</h3>
      <p class="muted">{runText(explanation)}</p>
      <p class="chips"><span class="chip">{coverageText(explanation.coverage, explanation.steps.length, explanation.warnings)}</span></p>
      <details>
        <summary>Why this order</summary>
        <ol class="steps">
          {#each stepsOf(explanation.steps) as r (r.position)}
            <li><span class="pos num">{r.position}</span><div class="body"><span>{r.title}</span><p class="text">{r.text}</p></div></li>
          {/each}
        </ol>
        <svg viewBox="0 0 {runCurve.width} {runCurve.height}" role="img" aria-label="Energy along the applied order">
          {#if runCurve.target}<path d={runCurve.target} class="target" fill="none" />{/if}
          <path d={runCurve.line} class="line" fill="none" />
        </svg>
      </details>
    </div>
  {/if}
</section>

<style>
  section {
    display: grid;
    gap: var(--sp-3);
  }
  h3 {
    margin: 0;
  }
  .form {
    display: grid;
    gap: var(--sp-2);
  }
  label {
    display: grid;
    gap: var(--sp-1);
  }
  select,
  button,
  textarea {
    min-height: var(--hit);
  }
  .hint {
    margin: 0;
  }
  .err {
    color: var(--warn);
    margin: 0;
  }
  .stale {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
    background: var(--warn-soft);
    border: 1px solid var(--warn);
    border-radius: var(--r-md);
    padding: var(--sp-2) var(--sp-3);
    margin: 0;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    margin: 0;
  }
  .cost p {
    margin: var(--sp-1) 0 0;
  }
  .bar {
    height: var(--sp-2);
    background: var(--bg-sunken);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--ok);
  }
  .warnings {
    color: var(--warn);
    padding-left: var(--sp-5);
    margin: 0;
  }
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  .steps li {
    display: flex;
    gap: var(--sp-3);
    padding: var(--sp-2) 0;
    border-bottom: 1px solid var(--line);
  }
  .pos {
    min-width: var(--sp-5);
    color: var(--text-2);
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .text {
    margin: var(--sp-1) 0;
    overflow-wrap: anywhere;
  }
  .ebar {
    display: block;
    height: var(--sp-1);
    background: var(--bg-sunken);
    border-radius: var(--r-md);
  }
  .ebar span {
    display: block;
    height: 100%;
    background: var(--accent);
    border-radius: var(--r-md);
  }
  svg {
    width: 100%;
    height: auto;
    background: var(--bg-sunken);
    border-radius: var(--r-md);
  }
  .line {
    stroke: var(--accent);
    stroke-width: 2;
  }
  .target {
    stroke: var(--text-2);
    stroke-width: 1;
    stroke-dasharray: 4 3;
  }
  circle {
    fill: var(--accent);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .pinned ul {
    margin: 0;
    padding-left: var(--sp-5);
  }
</style>
