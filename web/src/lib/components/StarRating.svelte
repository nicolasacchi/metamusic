<script lang="ts">
  import { starsFromKey } from "../judge.svelte";

  interface Props {
    value: number | null;
    onchange?: (stars: number | null) => void;
    readonly?: boolean;
    label?: string;
    /** Touch target edge in px: 44 by default, 52 where a sheet or lane uses the stars. */
    size?: 44 | 52;
  }
  let { value, onchange, readonly = false, label = "Rating", size = 44 }: Props = $props();

  function onkey(e: KeyboardEvent) {
    if (readonly || e.ctrlKey || e.metaKey || e.altKey) return;
    const n = starsFromKey(e.key);
    if (n === undefined) return;
    e.preventDefault();
    onchange?.(n);
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div class="stars" style:--star={`${size}px`} role="group" aria-label={label} onkeydown={onkey} data-testid="star-rating">
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
    gap: 0;
    align-items: center;
  }
  button {
    background: none;
    border: none;
    color: inherit;
    cursor: pointer;
    font-size: 1.5rem;
    min-width: 2rem;
    min-height: 2rem;
    padding: 0;
    border-radius: 0;
  }
  /* touch: 44 px stars (52 in a sheet or lane); a fine pointer keeps the compact desktop bar */
  @media (pointer: coarse) {
    button {
      min-width: var(--star, 44px);
      min-height: var(--star, 44px);
    }
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
