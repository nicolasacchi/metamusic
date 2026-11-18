<script lang="ts">
  import { STRENGTH_HINTS } from "../modePanels";

  interface Props {
    style: string;
    lyrics: string;
    /** The strength as text, 0..1. */
    strength: string;
  }
  let { style = $bindable(), lyrics = $bindable(), strength = $bindable() }: Props = $props();

  const shown = $derived(Number.isFinite(Number(strength)) && strength.trim() !== "" ? Number(strength) : 0.5);
</script>

<div data-testid="cover-fields">
  <label>New style <textarea bind:value={style} rows="2" placeholder="slow jazz trio, brushed drums" data-testid="panel-style"></textarea></label>
  <label>Lyrics (optional) <textarea bind:value={lyrics} rows="4" data-testid="panel-lyrics"></textarea></label>
  <label>Strength {shown.toFixed(2)}
    <input type="range" min="0" max="1" step="0.05" value={shown} oninput={(e) => (strength = e.currentTarget.value)} data-testid="cover-strength-slider" />
    <input type="text" inputmode="decimal" bind:value={strength} data-testid="cover-strength" />
  </label>
  <p class="muted" data-testid="cover-hints">
    {#each STRENGTH_HINTS as h, i (h.value)}{i > 0 ? " · " : ""}<button type="button" class="hint" onclick={() => (strength = String(h.value))}>{h.label}</button>{/each}
  </p>
  <p class="muted">The result keeps the take's structure and melody; a higher strength stays closer to the original.</p>
</div>

<style>
  label {
    display: grid;
    gap: 0.2rem;
    margin: 0.5rem 0;
    min-width: 0;
  }
  input:not([type="range"]),
  textarea {
    min-height: var(--hit);
    width: 100%;
    max-width: 100%;
    box-sizing: border-box;
  }
  .hint {
    min-height: var(--hit);
    min-width: var(--hit);
    background: none;
    border: none;
    text-decoration: underline;
    cursor: pointer;
    color: inherit;
  }
</style>
