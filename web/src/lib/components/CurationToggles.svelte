<script lang="ts">
  // The two curation opt-ins. Read-only (no onchange): the state and its effect in plain words.
  // With onchange (the secure page): one switch per setting, each change sends only its own key.
  import { TOGGLES, valueOf, type CurationKey, type CurationSettings } from "../curationView";

  interface Props {
    settings: Pick<CurationSettings, CurationKey>;
    onchange?(key: CurationKey, value: boolean): void;
    /** A change is being saved. */
    busy?: boolean;
  }
  let { settings, onchange, busy = false }: Props = $props();
</script>

<ul class="toggles" data-testid="curation-toggles">
  {#each TOGGLES as t (t.key)}
    {@const on = valueOf(settings, t.key)}
    <li class="toggle" data-testid="curation-row-{t.key}">
      <div class="head">
        <span class="label" id="curation-label-{t.key}">{t.label}</span>
        {#if onchange}
          <button
            type="button"
            role="switch"
            class="switch"
            aria-checked={on}
            aria-labelledby="curation-label-{t.key}"
            disabled={busy}
            data-testid="curation-toggle-{t.key}"
            onclick={() => onchange(t.key, !on)}><span class="knob" aria-hidden="true"></span></button
          >
        {:else}
          <span class="state" class:on data-testid="curation-toggle-{t.key}">{on ? "On" : "Off"}</span>
        {/if}
      </div>
      <p class="effect">{on ? t.on : t.off}</p>
      {#if t.note}<p class="note">{t.note}</p>{/if}
    </li>
  {/each}
</ul>

<style>
  .toggles {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-4);
  }
  .toggle {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    padding: var(--sp-4);
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-4);
    min-height: var(--hit);
  }
  .label {
    font-weight: var(--fw-bold);
  }
  .state {
    padding: var(--sp-1) var(--sp-3);
    border-radius: var(--r-pill);
    background: var(--bg-sunken);
    color: var(--text-2);
    font-size: var(--fs-sm);
  }
  .state.on {
    background: var(--accent-soft);
    color: var(--accent);
    font-weight: var(--fw-bold);
  }
  .switch {
    flex: none;
    position: relative;
    width: 52px;
    min-width: var(--hit);
    height: var(--hit);
    border-radius: var(--r-pill);
    border: 1px solid var(--line-strong);
    background: var(--bg-sunken);
    padding: 0;
  }
  .knob {
    position: absolute;
    top: 50%;
    left: 6px;
    width: 24px;
    height: 24px;
    margin-top: -12px;
    border-radius: var(--r-pill);
    background: var(--text-2);
    transition: transform var(--dur-2) var(--ease-out);
  }
  .switch[aria-checked="true"] {
    background: var(--accent-soft);
    border-color: var(--accent);
  }
  .switch[aria-checked="true"] .knob {
    background: var(--accent);
    transform: translateX(20px);
  }
  .switch:disabled {
    opacity: 0.6;
  }
  .effect {
    margin: var(--sp-2) 0 0;
  }
  .note {
    margin: var(--sp-2) 0 0;
    color: var(--text-3);
    font-size: var(--fs-sm);
  }
</style>
