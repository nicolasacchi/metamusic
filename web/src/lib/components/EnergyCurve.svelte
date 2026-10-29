<script lang="ts">
  import type { components } from "../../api/schema";
  import { energyModel } from "../energy";

  interface Props {
    energy: components["schemas"]["Energy"] | null | undefined;
  }
  let { energy }: Props = $props();
  const W = 600;
  const H = 160;
  const model = $derived(energy ? energyModel(energy, W, H) : null);
</script>

<section aria-label="Energy" data-testid="record-energy">
  {#if !model || model.lines.length === 0}
    <p class="muted">No loudness analysis yet.</p>
  {:else}
    <svg viewBox="0 0 {W} {H + 16}" role="img" aria-label="Loudness over the record, one line per slot">
      {#each model.ticks as t (t.lufs)}
        <line x1="0" x2={W} y1={t.y} y2={t.y} class="grid" />
        <text x="2" y={t.y - 2} class="tick">{t.lufs}</text>
      {/each}
      {#each model.lines as l, i (l.slotId)}
        <path d={l.path} class="line" class:alt={i % 2 === 1} fill="none" />
        {#if l.meanY !== null}<line x1={l.startX} x2={l.endX} y1={l.meanY} y2={l.meanY} class="mean" />{/if}
        {#if l.bpmLabel}<text x={l.startX + 2} y={H + 12} class="tick">{l.bpmLabel}</text>{/if}
      {/each}
    </svg>
  {/if}
  {#if model}
    <details>
      <summary>Table</summary>
      <table>
        <thead><tr><th>Slot</th><th>Mean</th><th>BPM</th><th>Length</th></tr></thead>
        <tbody>
          {#each model.table as r (r.slotId)}
            <tr><td>{r.position}</td><td>{r.mean}</td><td>{r.bpm}</td><td>{r.duration}</td></tr>
          {/each}
        </tbody>
      </table>
    </details>
  {/if}
</section>

<style>
  svg {
    width: 100%;
    height: auto;
    background: var(--bg-sunken);
    border-radius: var(--r-md);
  }
  .grid {
    stroke: var(--line);
    stroke-width: 1;
  }
  .tick {
    fill: var(--text-2);
    font-size: 10px;
  }
  .line {
    stroke: var(--accent);
    stroke-width: 2;
  }
  .line.alt {
    stroke: var(--info);
  }
  .mean {
    stroke: var(--text-2);
    stroke-width: 1;
    stroke-dasharray: 3 3;
  }
  details {
    margin-top: var(--sp-3);
  }
  summary {
    min-height: var(--hit);
    display: flex;
    align-items: center;
    cursor: pointer;
  }
  table {
    border-collapse: collapse;
    width: 100%;
  }
  th,
  td {
    text-align: left;
    padding: var(--sp-1) var(--sp-2);
    border-bottom: 1px solid var(--line);
  }
</style>
