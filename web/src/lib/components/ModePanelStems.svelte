<script lang="ts">
  import { STEM_NAMES, type StemName } from "../modePanels";

  interface Props {
    tracks: StemName[];
    /** The most names one item carries. */
    max: number;
  }
  let { tracks = $bindable(), max }: Props = $props();

  function toggle(n: StemName, on: boolean) {
    tracks = on ? [...tracks.filter((t) => t !== n), n] : tracks.filter((t) => t !== n);
  }
</script>

<fieldset class="stems" data-testid="stem-list">
  <legend>Track names (up to {max})</legend>
  {#each STEM_NAMES as n (n)}
    <label class="stem">
      <input type="checkbox" checked={tracks.includes(n)} disabled={!tracks.includes(n) && tracks.length >= max} onchange={(e) => toggle(n, e.currentTarget.checked)} data-testid="stem-{n}" />
      <span>{n.replaceAll("_", " ")}</span>
    </label>
  {/each}
</fieldset>

<style>
  .stems {
    border: 1px solid var(--line);
    border-radius: var(--r-sm);
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(9rem, 1fr));
    gap: 0 0.5rem;
    min-width: 0;
    max-width: 100%;
  }
  .stem {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-height: var(--hit);
  }
  .stem input {
    width: 1.4rem;
    height: 1.4rem;
  }
</style>
