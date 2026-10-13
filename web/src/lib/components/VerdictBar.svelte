<script lang="ts">
  import type { Verdict } from "../duelKeys";

  interface Props {
    chosen: Verdict | null;
    note?: string;
    disabled?: boolean;
    busy?: boolean;
    onchoose?: (v: Verdict) => void;
    onsubmit?: () => void;
  }
  let { chosen = $bindable(null), note = $bindable(""), disabled = false, busy = false, onchoose, onsubmit }: Props = $props();

  const options: { v: Verdict; label: string; key: string }[] = [
    { v: "a", label: "A better", key: "1" },
    { v: "b", label: "B better", key: "2" },
    { v: "tie", label: "Tie", key: "3" },
    { v: "both_bad", label: "Both bad", key: "4" },
  ];
</script>

<div class="verdict" data-testid="verdict-bar">
  <div class="opts" role="group" aria-label="Verdict">
    {#each options as o (o.v)}
      <button
        type="button"
        class:on={chosen === o.v}
        aria-pressed={chosen === o.v}
        {disabled}
        data-testid="verdict-{o.v}"
        onclick={() => {
          chosen = o.v;
          onchoose?.(o.v);
        }}>{o.label} <kbd>{o.key}</kbd></button
      >
    {/each}
  </div>
  <label class="note">
    Note
    <textarea rows="2" maxlength="2000" bind:value={note} {disabled} placeholder="Optional"></textarea>
  </label>
  <button type="button" class="submit" data-testid="verdict-submit" disabled={disabled || busy || chosen === null} onclick={() => onsubmit?.()}>
    {busy ? "Submitting…" : "Submit"}
  </button>
</div>

<style>
  .verdict {
    display: grid;
    gap: 0.75rem;
  }
  .opts {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 0.5rem;
  }
  button {
    min-height: 48px;
    cursor: pointer;
  }
  button.on {
    outline: 3px solid #f2a65a;
  }
  .note {
    display: grid;
    gap: 0.25rem;
  }
  kbd {
    opacity: 0.6;
    font-size: 0.75em;
  }
  .submit {
    width: 100%;
  }
</style>
