<script lang="ts">
  // The notation of a plan (DESK-W1, C363.1): abcjs draws it, a click on a note selects its bar. Notation only; listening to
  // the plan is the server's audition (DESK-W2). abcjs is loaded on demand, so a failed load says so and the plan stays readable
  // as text. For a YuE2 score that reads differently under the standard accidental rule the display rewrite is drawn
  // (D-DESK-15); its bars are the same bars, so a click names the same voice and bar either way.
  import { untrack } from "svelte";
  import { markBar, renderScore, type AbcLoader, type BarRef } from "../abcRender";
  import type { Plan } from "../deskApi";
  import { voiceIds } from "../deskView";

  interface Props {
    plan: Pick<Plan, "abc" | "bar_spans" | "display" | "score">;
    selected?: BarRef | null;
    onselect?: (b: BarRef) => void;
    load?: AbcLoader;
  }
  let { plan, selected = null, onselect, load }: Props = $props();

  let host = $state<HTMLDivElement | null>(null);
  let failed = $state(false);
  let drawn = $state(0);

  const shown = $derived(plan.display ? { abc: plan.display.abc, spans: plan.display.bar_spans ?? [] } : { abc: plan.abc ?? "", spans: plan.bar_spans ?? [] });
  const voices = $derived(voiceIds(plan));

  $effect(() => {
    const el = host;
    const { abc, spans } = shown;
    if (!el || abc === "") return;
    let live = true;
    failed = false;
    untrack(() => {
      renderScore(el, { abc, spans, load, onBar: (b) => onselect?.(b) }).then(
        () => live && drawn++,
        () => live && (failed = true),
      );
    });
    return () => {
      live = false;
    };
  });
  $effect(() => {
    void drawn;
    if (host) markBar(host, voices, selected);
  });
</script>

{#if failed}
  <p class="muted" role="status" data-testid="score-notation-failed">The notation could not be loaded. The score text below is complete.</p>
{/if}
<div class="paper" bind:this={host} data-testid="score-view" aria-label="Score notation" role="img"></div>

<style>
  .paper {
    background: #f6f3ec;
    color: #111;
    border-radius: var(--radius, 6px);
    padding: var(--sp-2);
    overflow-x: auto;
    max-width: 100%;
    box-sizing: border-box;
    min-height: 3rem;
  }
  .paper :global(svg) {
    max-width: 100%;
    height: auto;
    cursor: pointer;
  }
  .paper :global(.mm-bar-selected) {
    fill: #b3261e;
    stroke: #b3261e;
  }
</style>
