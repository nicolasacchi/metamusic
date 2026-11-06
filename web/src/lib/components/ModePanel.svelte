<script lang="ts">
  import type { EngineCaps } from "../engines";
  import { MAX_COMPLETE_TRACKS, MAX_STEM_TRACKS, type EngineMode, type GenerationIntent } from "../genIntent";
  import { buildPanel, MODE_TITLES, panelFromIntent, TIME_SIGNATURES } from "../modePanels";
  import ModePanelStems from "./ModePanelStems.svelte";

  interface Props {
    mode: EngineMode;
    intent: GenerationIntent;
    /** The ACE-Step caps from list_engines; null when not loaded (limits are then the server's to enforce). */
    caps: EngineCaps | null;
    onsubmit: (i: GenerationIntent) => void;
  }
  let { mode, intent, caps, onsubmit }: Props = $props();

  /* svelte-ignore state_referenced_locally */
  let st = $state(panelFromIntent(intent));
  let error = $state("");

  const hasPatch = $derived(mode === "port" || mode === "reference");

  function submit(e: Event) {
    e.preventDefault();
    const r = buildPanel(mode, intent, st, caps);
    if (r.ok) {
      error = "";
      onsubmit(r.intent);
    } else error = r.error;
  }
</script>

<form onsubmit={submit} data-testid="mode-panel-{mode}">
  <h2>{MODE_TITLES[mode]}</h2>
  {#if mode === "reference"}
    <p class="muted">Reference take <code>{intent.reference?.track_id}</code></p>
  {:else if intent.parent_track_id}
    <p class="muted">Source take <a href="/tracks/{intent.parent_track_id}">{intent.parent_track_id}</a></p>
  {/if}

  {#if mode === "repaint" || mode === "reference"}
    <div class="pair">
      <label>{mode === "repaint" ? "From (s)" : "Window from (s)"}
        <input type="text" inputmode="decimal" bind:value={st.from} data-testid="range-from" />
      </label>
      <label>{mode === "repaint" ? "To (s)" : "Window to (s)"}
        <input type="text" inputmode="decimal" bind:value={st.to} data-testid="range-to" />
      </label>
    </div>
    {#if mode === "repaint" && caps && (caps.repaint_min_s > 0 || caps.repaint_max_s > 0)}
      <p class="muted" data-testid="range-limits">Repaint {caps.repaint_min_s}–{caps.repaint_max_s} s. Times round to 0.1 s.</p>
    {:else if mode === "reference"}
      <p class="muted" data-testid="range-limits">Leave both empty for the last 20 s{caps && caps.reference_max_s > 0 ? `; at most ${caps.reference_max_s} s` : ""}.</p>
    {/if}
  {/if}

  {#if mode === "extend"}
    <label>Add (s)
      <input type="text" inputmode="decimal" bind:value={st.extend} data-testid="extend-s" />
    </label>
    {#if caps && caps.extend_max_s > 0}<p class="muted" data-testid="extend-limits">Between {caps.extend_min_s} and {caps.extend_max_s} s. The seam is experimental.</p>{/if}
  {/if}

  {#if mode === "complete" || mode === "stems"}
    <ModePanelStems bind:tracks={st.tracks} max={mode === "complete" ? MAX_COMPLETE_TRACKS : MAX_STEM_TRACKS} />
    {#if mode === "stems"}<p class="muted">One job per name.</p>{/if}
  {/if}

  {#if hasPatch}
    <label>Style <textarea bind:value={st.style} rows="2" data-testid="panel-style"></textarea></label>
    <label>Lyrics <textarea bind:value={st.lyrics} rows="6" data-testid="panel-lyrics"></textarea></label>
    <div class="pair">
      <label>Tempo (bpm) <input type="text" inputmode="numeric" bind:value={st.bpm} data-testid="panel-bpm" /></label>
      <label>Key <input type="text" bind:value={st.keyscale} placeholder="C major" data-testid="panel-key" /></label>
    </div>
    <div class="pair">
      <label>Time signature
        <select bind:value={st.timesignature} data-testid="panel-timesig">
          <option value="">Default</option>
          {#each TIME_SIGNATURES as t (t)}<option value={t}>{t}/4</option>{/each}
        </select>
      </label>
      <label>Duration (s) <input type="text" inputmode="decimal" bind:value={st.duration} data-testid="panel-duration" /></label>
    </div>
    <label class="check"><input type="checkbox" bind:checked={st.think} data-testid="panel-think" /> <span>Let the model plan first (chain of thought)</span></label>
  {/if}

  <div class="pair">
    <label>Takes <input type="number" min="1" max="50" bind:value={st.nSeeds} data-testid="n-seeds" /></label>
    <label>Or explicit seeds <input type="text" bind:value={st.seedsText} placeholder="1, 2, 3" data-testid="seeds" /></label>
  </div>

  {#if error}<p role="alert" data-testid="panel-error">{error}</p>{/if}
  <p><button type="submit" class="primary" data-testid="estimate-button">Estimate</button></p>
</form>

<style>
  form {
    min-width: 0;
    max-width: 40rem;
  }
  label {
    display: grid;
    gap: 0.2rem;
    margin: 0.5rem 0;
    min-width: 0;
  }
  input:not([type="checkbox"]),
  select,
  textarea {
    min-height: var(--hit);
    width: 100%;
    max-width: 100%;
    box-sizing: border-box;
  }
  .pair {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 9rem), 1fr));
    gap: 0 0.75rem;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-height: var(--hit);
  }
  .check input {
    width: 1.4rem;
    height: 1.4rem;
  }
  button {
    min-height: var(--hit);
    min-width: var(--hit);
  }
</style>
