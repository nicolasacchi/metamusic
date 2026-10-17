<script lang="ts">
  import { starsFromKey } from "../judge.svelte";

  interface Props {
    value: number | null;
    onchange?: (stars: number | null) => void;
    readonly?: boolean;
    label?: string;
    /** Touch size of each star in px (the now-playing sheet uses 52); default is the compact desktop size. */
    size?: number;
  }
  let { value, onchange, readonly = false, label = "Rating", size }: Props = $props();

  function onkey(e: KeyboardEvent) {
    if (readonly || e.ctrlKey || e.metaKey || e.altKey) return;
    const n = starsFromKey(e.key);
    if (n === undefined) return;
    e.preventDefault();
    onchange?.(n);
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div class="stars" role="group" aria-label={label} onkeydown={onkey} data-testid="star-rating" style={size ? `--star-hit:${size}px;--star-fs:${Math.round(size * 0.62)}px` : undefined}>
  {#each [1, 2, 3, 4, 5] as n (n)}
    <button
      type="button"
      class="star"
      class:on={value !== null && n <= value}
      aria-label="{n} {n === 1 ? 'star' : 'stars'}"
      aria-pressed={value === n}
      disabled={readonly}
      onclick={() => onchange?.(value === n ? null : n)}>{value !== null && n <= value ? "★" : "☆"}</button
    >
  {/each}
  {#if !readonly}
    <button type="button" class="clear" aria-label="Clear rating" disabled={value === null} onclick={() => onchange?.(null)}>×</button>
  {/if}
</div>

<style>
  .stars {
    display: inline-flex;
    gap: 0.1rem;
    align-items: center;
  }
  button {
    background: none;
    border: none;
    color: inherit;
    cursor: pointer;
    font-size: var(--star-fs, 1.25rem);
    min-width: var(--star-hit, 2rem);
    min-height: var(--star-hit, 2rem);
    padding: 0;
  }
  button:disabled {
    cursor: default;
  }
  .star.on {
    color: #f2a65a;
  }
  .clear {
    font-size: 1rem;
    opacity: 0.7;
  }
</style>
