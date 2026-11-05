<script lang="ts">
  import type { PickerChoice } from "../engines";

  interface Props {
    choices: PickerChoice[];
    value: string;
    onpick: (name: string) => void;
    /** A status line under the group (loading, load failure). */
    note?: string;
  }
  let { choices, value, onpick, note = "" }: Props = $props();
</script>

<fieldset class="engines" data-testid="engine-picker">
  <legend>Engine</legend>
  {#each choices as c (c.name)}
    <label class="choice" class:off={!c.enabled} data-testid="engine-{c.name}">
      <input type="radio" name="engine" value={c.name} checked={value === c.name} disabled={!c.enabled} onchange={() => onpick(c.name)} />
      <span class="body">
        <span class="label">{c.label}</span>
        {#if c.reason}<span class="muted reason" data-testid="engine-reason-{c.name}">{c.reason}</span>{/if}
        {#if c.hint}<span class="muted hint" data-testid="engine-hint-{c.name}">How to adopt: <code>{c.hint}</code></span>{/if}
      </span>
    </label>
  {/each}
  {#if note}<p class="muted" data-testid="engine-note">{note}</p>{/if}
</fieldset>

<style>
  .engines {
    border: 1px solid var(--line);
    border-radius: var(--r-sm);
    margin: 0.5rem 0;
    padding: 0.25rem 0.5rem;
    min-width: 0;
    max-width: 100%;
  }
  .choice {
    display: flex;
    align-items: flex-start;
    gap: 0.6rem;
    min-height: var(--hit);
    padding: 0.4rem 0;
  }
  .choice input {
    flex: none;
    width: 1.4rem;
    height: 1.4rem;
    margin-top: 0.1rem;
  }
  .body {
    display: grid;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .off .label {
    opacity: 0.7;
  }
</style>
