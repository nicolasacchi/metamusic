<script lang="ts">
  import { SONG_STATES, type BoardFilters } from "../boardView";
  import { tokenLabel } from "../soundsView";
  import type { components } from "../../api/schema";

  type Session = components["schemas"]["SessionSummary"];
  type Facets = components["schemas"]["SoundSearch"]["facets"];

  interface Props {
    filters: BoardFilters;
    sessions: Session[];
    facets: Facets;
    onchange: (next: BoardFilters) => void;
  }
  let { filters, sessions, facets, onchange }: Props = $props();

  let qText = $derived(filters.q);

  function toggleState(s: (typeof SONG_STATES)[number]) {
    const states = filters.states.includes(s) ? filters.states.filter((x) => x !== s) : [...filters.states, s];
    onchange({ ...filters, states });
  }
  function toggleSound(token: string) {
    const sounds = filters.sounds.includes(token) ? filters.sounds.filter((x) => x !== token) : [...filters.sounds, token];
    onchange({ ...filters, sounds });
  }
  const facetNames = $derived(Object.keys(facets).sort());
</script>

<div class="bar" role="search" aria-label="Board filters">
  <label>
    Session
    <select value={filters.session} onchange={(e) => onchange({ ...filters, session: e.currentTarget.value })} data-testid="filter-session">
      <option value="">All sessions</option>
      {#each sessions as s (s.id)}
        <option value={s.id}>{s.name} ({s.songs})</option>
      {/each}
    </select>
  </label>
  <div class="states" role="group" aria-label="State">
    {#each SONG_STATES as s (s)}
      <button type="button" aria-pressed={filters.states.includes(s)} onclick={() => toggleState(s)} data-testid="filter-state-{s}">{s}</button>
    {/each}
  </div>
  <form
    onsubmit={(e) => {
      e.preventDefault();
      onchange({ ...filters, q: qText.trim() });
    }}
  >
    <input type="search" bind:value={qText} placeholder="Search songs" aria-label="Search songs" data-testid="filter-q" />
  </form>
  {#if filters.angle}
    <button type="button" class="pill" onclick={() => onchange({ ...filters, angle: "" })}>angle: {filters.angle} ×</button>
  {/if}
  {#if filters.muse}
    <button type="button" class="pill" onclick={() => onchange({ ...filters, muse: "" })}>one Muse ×</button>
  {/if}
</div>
{#if filters.sounds.length > 0 || facetNames.length > 0}
  <div class="facets" aria-label="Sound facets">
    {#each filters.sounds as token (token)}
      <button type="button" class="pill on" aria-pressed="true" data-sound={token} onclick={() => toggleSound(token)}>{tokenLabel(token)} ×</button>
    {/each}
    {#each facetNames as f (f)}
      {#each facets[f] ?? [] as v (v.value)}
        {#if !filters.sounds.includes(`${f}:${v.value}`)}
          <button type="button" class="pill" aria-pressed="false" data-sound="{f}:{v.value}" onclick={() => toggleSound(`${f}:${v.value}`)}>{tokenLabel(`${f}:${v.value}`)} <span class="muted">{v.count}</span></button>
        {/if}
      {/each}
    {/each}
  </div>
{/if}

<style>
  .bar,
  .facets {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 0.75rem;
    align-items: center;
    margin-bottom: 0.5rem;
  }
  .states {
    display: flex;
    gap: 0.25rem;
  }
  button[aria-pressed="true"] {
    background: #3a322b;
    outline: 1px solid #f2a65a;
  }
  .pill {
    border-radius: 1rem;
    font-size: 0.8rem;
  }
</style>
