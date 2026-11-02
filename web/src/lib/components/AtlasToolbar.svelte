<script lang="ts">
  import { AXES, COLOR_BYS, DEFAULT_X, DEFAULT_Y, PRESETS, activePreset } from "../atlasApi";
  import { SONG_STATES, type AtlasFilter } from "../atlasView";
  import { tokenLabel } from "../soundsView";
  import type { components } from "../../api/schema";
  import MusePicker from "./MusePicker.svelte";

  type Session = components["schemas"]["SessionSummary"];
  type Facets = components["schemas"]["SoundSearch"]["facets"];
  type Crate = { id: string; name: string };

  interface Props {
    filter: AtlasFilter;
    sessions: readonly Session[];
    facets: Facets;
    crates: readonly Crate[];
    /** The server's AXIS_PAIR_INVALID text for an equal pair ("" when fine). */
    axisError?: string;
    hidePredicted: boolean;
    selectMode: boolean;
    /** Show the touch Select toggle (a phone or any coarse pointer). */
    touch?: boolean;
    onchange: (next: AtlasFilter) => void;
    onhidepredicted: (hide: boolean) => void;
    onselectmode: (on: boolean) => void;
  }
  let { filter, sessions, facets, crates, axisError = "", hidePredicted, selectMode, touch = false, onchange, onhidepredicted, onselectmode }: Props = $props();

  let qText = $derived(filter.q);
  let picking = $state(false);

  const x = $derived(filter.x || DEFAULT_X);
  const y = $derived(filter.y || DEFAULT_Y);
  const preset = $derived(activePreset(filter));
  const facetNames = $derived(Object.keys(facets).sort());

  function toggleState(s: (typeof SONG_STATES)[number]) {
    const states = filter.states.includes(s) ? filter.states.filter((v) => v !== s) : [...filter.states, s];
    onchange({ ...filter, states });
  }
  function toggleSound(token: string) {
    const sounds = filter.sounds.includes(token) ? filter.sounds.filter((v) => v !== token) : [...filter.sounds, token];
    onchange({ ...filter, sounds });
  }
</script>

<div class="bar" role="toolbar" aria-label="Atlas view">
  <div class="presets" role="group" aria-label="Presets">
    {#each PRESETS as p (p.name)}
      <button type="button" aria-pressed={preset === p.name} onclick={() => onchange({ ...filter, x: p.x, y: p.y })} data-testid="atlas-preset-{p.name}">{p.label}</button>
    {/each}
  </div>
  <label>
    X axis
    <select value={x} onchange={(e) => onchange({ ...filter, x: e.currentTarget.value })} data-testid="atlas-x">
      {#each AXES as a (a)}<option value={a}>{a}</option>{/each}
    </select>
  </label>
  <label>
    Y axis
    <select value={y} onchange={(e) => onchange({ ...filter, y: e.currentTarget.value })} data-testid="atlas-y">
      {#each AXES as a (a)}<option value={a}>{a}</option>{/each}
    </select>
  </label>
  <label>
    Colour by
    <select value={filter.color || "none"} onchange={(e) => onchange({ ...filter, color: e.currentTarget.value === "none" ? "" : e.currentTarget.value })} data-testid="atlas-color">
      {#each COLOR_BYS as c (c)}<option value={c}>{c}</option>{/each}
    </select>
  </label>
  <label class="check"><input type="checkbox" checked={filter.dim} onchange={(e) => onchange({ ...filter, dim: e.currentTarget.checked })} data-testid="atlas-dim" /> Dim non-matching</label>
  <label class="check"><input type="checkbox" checked={hidePredicted} onchange={(e) => onhidepredicted(e.currentTarget.checked)} data-testid="atlas-hide-predicted" /> Hide predicted points</label>
  {#if touch}
    <button type="button" aria-pressed={selectMode} onclick={() => onselectmode(!selectMode)} data-testid="atlas-select-toggle">{selectMode ? "Done selecting" : "Select"}</button>
  {/if}
</div>
{#if axisError}<p role="alert" class="err" data-testid="atlas-axis-error">{axisError}</p>{/if}

<div class="bar" role="search" aria-label="Atlas filters">
  <label>
    Session
    <select value={filter.session} onchange={(e) => onchange({ ...filter, session: e.currentTarget.value })} data-testid="atlas-filter-session">
      <option value="">All sessions</option>
      {#each sessions as s (s.id)}<option value={s.id}>{s.name} ({s.songs})</option>{/each}
    </select>
  </label>
  <label>
    Crate
    <select value={filter.crate} onchange={(e) => onchange({ ...filter, crate: e.currentTarget.value })} data-testid="atlas-filter-crate">
      <option value="">Whole library</option>
      {#each crates as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
      {#if filter.crate && !crates.some((c) => c.id === filter.crate)}<option value={filter.crate}>this Crate</option>{/if}
    </select>
  </label>
  <div class="states" role="group" aria-label="State">
    {#each SONG_STATES as s (s)}
      <button type="button" aria-pressed={filter.states.includes(s)} onclick={() => toggleState(s)} data-testid="atlas-state-{s}">{s}</button>
    {/each}
  </div>
  <form
    onsubmit={(e) => {
      e.preventDefault();
      onchange({ ...filter, q: qText.trim() });
    }}
  >
    <input type="search" bind:value={qText} placeholder="Search songs" aria-label="Search songs" data-testid="atlas-filter-q" />
  </form>
  {#if filter.muse}
    <button type="button" class="pill on" onclick={() => onchange({ ...filter, muse: "" })} data-testid="atlas-muse-pill">one Muse ×</button>
  {:else}
    <button type="button" class="pill" aria-expanded={picking} onclick={() => (picking = !picking)} data-testid="atlas-muse-open">Muse…</button>
  {/if}
  {#if filter.near}
    <button type="button" class="pill on" onclick={() => onchange({ ...filter, near: "" })} data-testid="atlas-near-pill">near a Song ×</button>
  {/if}
</div>
{#if picking && !filter.muse}
  <MusePicker
    onpick={(m) => {
      picking = false;
      onchange({ ...filter, muse: m.id });
    }}
    oncancel={() => (picking = false)}
  />
{/if}
{#if filter.sounds.length > 0 || facetNames.length > 0}
  <div class="facets" aria-label="Sound facets">
    {#each filter.sounds as token (token)}
      <button type="button" class="pill on" aria-pressed="true" data-sound={token} onclick={() => toggleSound(token)}>{tokenLabel(token)} ×</button>
    {/each}
    {#each facetNames as f (f)}
      {#each facets[f] ?? [] as v (v.value)}
        {#if !filter.sounds.includes(`${f}:${v.value}`)}
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
    gap: var(--sp-2) var(--sp-3);
    align-items: center;
    margin-bottom: var(--sp-2);
  }
  .presets,
  .states {
    display: flex;
    gap: var(--sp-1);
    flex-wrap: wrap;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: var(--fs-xs);
    color: var(--text-2);
  }
  label.check {
    flex-direction: row;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
    font-size: var(--fs-sm);
    color: var(--text);
  }
  label.check input {
    width: 24px;
    height: 24px;
  }
  button,
  select,
  input[type="search"] {
    min-height: var(--hit);
  }
  button[aria-pressed="true"],
  .pill.on {
    background: var(--accent-soft);
    outline: 1px solid var(--accent);
  }
  .pill {
    border-radius: 1rem;
    font-size: var(--fs-sm);
  }
  .err {
    margin: 0 0 var(--sp-2);
    color: var(--text);
    border-left: 3px solid var(--accent);
    padding-left: var(--sp-2);
  }
</style>
