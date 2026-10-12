<script lang="ts">
  import type { components } from "../../api/schema";
  import { sectionAt } from "../sections";
  import { fmt } from "../time";

  type Section = components["schemas"]["SectionOut"];
  interface Props {
    sections: readonly Section[];
    timed: boolean;
    /** Playback position in seconds, or null when this track is not the one playing (no highlight). */
    current: number | null;
    onseek: (seconds: number) => void;
  }
  let { sections, timed, current, onseek }: Props = $props();

  // Highlight only when section times exist; untimed tracks show structure alone.
  const active = $derived(timed && current !== null ? sectionAt(sections, current) : -1);
</script>

<div class="lyrics" data-testid="lyrics">
  {#if !timed}<p class="muted note" data-testid="untimed-note">section times need a score</p>{/if}
  {#each sections as s, i (s.idx)}
    <section class="sec" class:active={i === active} data-testid="lyrics-section" data-active={i === active ? "true" : "false"} aria-current={i === active ? "true" : undefined}>
      <h3>
        {#if s.start_s !== null}
          <button type="button" class="seek" onclick={() => onseek(s.start_s ?? 0)} title="Play from {fmt(s.start_s)}">{s.label}</button>
          <span class="muted time">{fmt(s.start_s)}</span>
        {:else}
          {s.label}
        {/if}
      </h3>
      {#if s.lines.length > 0}
        <p class="body">{#each s.lines as line, j (j)}{line + "\n"}{/each}</p>
      {/if}
    </section>
  {:else}
    <p class="muted">No lyrics.</p>
  {/each}
</div>

<style>
  .sec {
    padding: 0.25rem 0.6rem;
    border-left: 3px solid transparent;
    margin-bottom: 0.4rem;
  }
  .sec.active {
    border-left-color: #f2a65a;
    background: #231d17;
  }
  h3 {
    margin: 0 0 0.2rem;
    font-size: 0.95rem;
  }
  .body {
    margin: 0;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .seek {
    all: unset;
    cursor: pointer;
    text-decoration: underline;
  }
  .time {
    font-size: 0.75rem;
    margin-left: 0.4rem;
  }
  .note {
    font-size: 0.85rem;
  }
</style>
