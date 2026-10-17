<script lang="ts">
  import type { TrackStatus } from "../judge.svelte";

  interface Props {
    value: string;
    onchange: (status: TrackStatus) => void;
    label?: string;
  }
  let { value, onchange, label = "Status" }: Props = $props();

  const CHIPS: { status: TrackStatus; text: string }[] = [
    { status: "to_relisten", text: "To relisten" },
    { status: "final", text: "Final" },
    { status: "discarded", text: "Discard" },
  ];
  // Tapping the active chip clears the decision (back to draft).
  function tap(s: TrackStatus) {
    onchange(value === s ? "draft" : s);
  }
</script>

<div class="chips" role="group" aria-label={label} data-testid="status-chips">
  {#each CHIPS as c (c.status)}
    <button type="button" class="chip-btn" class:danger={c.status === "discarded"} aria-pressed={value === c.status} data-testid="status-{c.status}" onclick={() => tap(c.status)}>{c.text}</button>
  {/each}
</div>

<style>
  .chips {
    display: inline-flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .chip-btn {
    min-width: var(--hit);
    min-height: var(--hit);
    padding: 0 var(--sp-4);
  }
  .chip-btn[aria-pressed="true"] {
    background: var(--accent-soft);
    border-color: var(--accent);
    color: var(--accent);
    font-weight: var(--fw-bold);
  }
  .chip-btn.danger[aria-pressed="true"] {
    background: var(--bad-soft);
    border-color: var(--bad);
    color: var(--bad);
  }
</style>
