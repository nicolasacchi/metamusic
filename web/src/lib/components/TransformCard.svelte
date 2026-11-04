<script lang="ts">
  // One transform: kind, state chip, progress bar with phase, Cancel on live rows, link to its Take, render-time warnings
  // and the row error. Used by the Take panel (after Render) and the /transforms page.
  import {
    canCancel, cpuUseLine, isLive, kindLabel, phaseLabel, progressPercent, rowErrorText, stateLabel, stateTone, warningNote, warningTone, type TransformView,
  } from "../transformView";

  interface Props {
    transform: TransformView;
    busy?: boolean;
    oncancel?: (id: string) => void;
    /** "A/B with source" on a finished transform; only the Take panel offers it. */
    onab?: (id: string) => void;
    compact?: boolean;
  }
  let { transform: t, busy = false, oncancel, onab, compact = false }: Props = $props();
  const live = $derived(isLive(t.state));
  const pct = $derived(progressPercent(t.progress_permille));
</script>

<article class="card tcard" data-testid="transform-card" data-state={t.state} data-kind={t.kind} data-id={t.id}>
  <header>
    <b>{kindLabel(t.kind)}</b>
    <span class="chip {stateTone(t.state)}" data-testid="transform-state">{stateLabel(t.state)}</span>
    {#if t.reused}<span class="chip" title="This exact transform already existed">reused</span>{/if}
    {#if t.restored}<span class="chip" title="A discarded take of this transform was brought back">restored</span>{/if}
  </header>
  <p class="title">{t.title}</p>
  {#if !compact}
    <p class="muted src">
      {#each t.inputs as i, k (i.track_id + k)}{k > 0 ? " + " : ""}<a href="/tracks/{i.track_id}">{i.title || i.track_id}</a>{/each}
      · {cpuUseLine(t)}
    </p>
  {/if}
  {#if live}
    <div class="bar" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct} aria-label="Transform progress" data-testid="transform-progress">
      <i style:width="{pct}%"></i>
    </div>
    <p class="muted phase" data-testid="transform-phase">{pct}% · {phaseLabel(t.phase) || (t.state === "queued" ? "Waiting for a slot" : "Working")}{#if t.cancel_requested_at} · cancelling…{/if}</p>
  {/if}
  {#if t.error && t.state !== "cancelled"}<p class="err" role="alert" data-testid="transform-error">{rowErrorText(t.error)}</p>{/if}
  {#if t.warnings.length}
    <ul class="warnings" data-testid="transform-card-warnings">
      {#each t.warnings as w, k (k)}<li class={warningTone(w)} data-code={w.code}>{warningNote(w)}</li>{/each}
    </ul>
  {/if}
  <div class="actions">
    {#if t.state === "done"}
      <a class="btn" href="/tracks/{t.take_id}" data-testid="transform-open">Open the new take</a>
      {#if onab}<button type="button" disabled={busy} onclick={() => onab?.(t.id)} data-testid="transform-ab">A/B with source</button>{/if}
    {/if}
    {#if oncancel && canCancel(t)}
      <button type="button" class="danger" disabled={busy} onclick={() => oncancel?.(t.id)} data-testid="transform-cancel">Cancel</button>
    {/if}
  </div>
</article>

<style>
  .tcard {
    min-width: 0;
  }
  header {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
  }
  .title,
  .src,
  .err,
  .warnings {
    overflow-wrap: anywhere;
    margin: var(--sp-1) 0;
  }
  .warnings {
    padding-left: var(--sp-4);
  }
  .warnings .caution {
    color: var(--warn, #e0a030);
  }
  .err {
    color: var(--bad);
  }
  .bar {
    height: 0.6rem;
    border-radius: var(--r-pill);
    background: var(--bg-sunken);
    overflow: hidden;
    margin-top: var(--sp-2);
  }
  .bar i {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width var(--dur-2) var(--ease-out);
  }
  .phase {
    font-size: var(--fs-xs);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    margin-top: var(--sp-2);
  }
  .actions button,
  .actions .btn {
    min-height: var(--hit);
    display: inline-flex;
    align-items: center;
  }
  .btn {
    padding: var(--sp-1) var(--sp-3);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-pill);
    text-decoration: none;
  }
</style>
