<script lang="ts">
  // The fields of one transform kind, generated from its capabilities entry (C214.2). Kind-specific pickers: partner Take
  // (same lyrics, other styles), ensemble space preset buttons, downbeat "auto (guess)" toggle, dry level "off".
  import { isAuto, type Field, type FieldErrors, type FormState } from "../transformForm";
  import type { PartnerTake } from "../transformApi";

  interface Props {
    fields: Field[];
    form: FormState;
    errors: FieldErrors;
    partners: PartnerTake[];
    partnersLoading?: boolean;
  }
  let { fields, form = $bindable(), errors, partners, partnersLoading = false }: Props = $props();

  const OTHER = "__other";
  // svelte-ignore state_referenced_locally
  let other = $state(!!form.values.track_b_id && !partners.some((p) => p.id === form.values.track_b_id));
  const pickValue = $derived(other ? OTHER : (form.values.track_b_id ?? ""));
  function pick(v: string) {
    if (v === OTHER) {
      other = true;
      form.values.track_b_id = "";
    } else {
      other = false;
      form.values.track_b_id = v;
    }
  }
  function set(key: string, v: string) {
    form.values[key] = v;
  }
  function toggleAuto(key: string, on: boolean) {
    form.values[key] = on ? "auto" : "";
  }
</script>

<div class="fields" data-testid="transform-fields">
  <label class="wide">Name <span class="muted">(optional)</span>
    <input type="text" maxlength="200" value={form.title} oninput={(e) => (form.title = e.currentTarget.value)} data-testid="tf-title" />
  </label>
  {#each fields as f (f.key)}
    {@const err = errors[f.key]}
    {#if f.key === "track_b_id"}
      <div class="wide field" data-testid="tf-partner">
        <label for="tf-partner-select">Partner take <span class="muted">(same lyrics, other styles)</span></label>
        {#if partnersLoading}
          <p class="muted">Looking for takes with the same lyrics…</p>
        {:else if partners.length > 0}
          <select id="tf-partner-select" value={pickValue} onchange={(e) => pick(e.currentTarget.value)} aria-invalid={err ? "true" : undefined} data-testid="tf-partner-select">
            <option value="">Choose a take</option>
            {#each partners as p (p.id)}<option value={p.id}>{p.title || p.id}</option>{/each}
            <option value={OTHER}>Another take (enter its id)</option>
          </select>
        {:else}
          <p class="muted" data-testid="tf-no-partners">No other take has the same lyrics. Enter a take id.</p>
        {/if}
        {#if other || (!partnersLoading && partners.length === 0)}
          <input type="text" aria-label="Partner take id" value={form.values.track_b_id ?? ""} oninput={(e) => set("track_b_id", e.currentTarget.value)} autocomplete="off" spellcheck="false" data-testid="tf-partner-id" />
        {/if}
        {#if err}<small class="err" role="alert">{err}</small>{/if}
      </div>
    {:else if f.type === "select" && f.param === "space"}
      <div class="wide field" role="group" aria-label={f.label}>
        <span class="lab">{f.label}</span>
        <div class="presets" data-testid="tf-presets">
          {#each f.options ?? [] as o (o)}
            <button type="button" class="preset" aria-pressed={(form.values[f.key] || f.placeholder) === o} onclick={() => set(f.key, o)} data-testid="tf-preset-{o}">{o}</button>
          {/each}
        </div>
      </div>
    {:else if f.type === "select"}
      <label class="field">{f.label}
        <select value={form.values[f.key] ?? ""} onchange={(e) => set(f.key, e.currentTarget.value)} aria-invalid={err ? "true" : undefined} data-testid="tf-{f.key}">
          <option value="">default{f.placeholder ? ` (${f.placeholder})` : ""}</option>
          {#each f.options ?? [] as o (o)}<option value={o}>{o}</option>{/each}
        </select>
        {#if err}<small class="err" role="alert">{err}</small>{/if}
      </label>
    {:else if f.type === "boolean"}
      <label class="field check"><input type="checkbox" checked={form.values[f.key] === "true"} onchange={(e) => set(f.key, e.currentTarget.checked ? "true" : "")} data-testid="tf-{f.key}" /> {f.label}</label>
    {:else if f.type === "downbeat"}
      <div class="field">
        <label for="tf-{f.key}">{f.label}{#if f.required} <span class="req">required</span>{/if}</label>
        <input id="tf-{f.key}" type="number" inputmode="decimal" min="0" step="0.01" disabled={isAuto(form, f.key)} value={isAuto(form, f.key) ? "" : (form.values[f.key] ?? "")} oninput={(e) => set(f.key, e.currentTarget.value)} placeholder={isAuto(form, f.key) ? "auto" : "from the score grid"} aria-invalid={err ? "true" : undefined} data-testid="tf-{f.key}" />
        <label class="check small"><input type="checkbox" checked={isAuto(form, f.key)} onchange={(e) => toggleAuto(f.key, e.currentTarget.checked)} data-testid="tf-{f.key}-auto" /> auto (guess)</label>
        {#if err}<small class="err" role="alert">{err}</small>{/if}
      </div>
    {:else if f.type === "dryDb"}
      <div class="field">
        <label for="tf-{f.key}">{f.label}</label>
        <input id="tf-{f.key}" type="number" inputmode="decimal" step={f.step} min={f.min} max={f.max} disabled={form.values[f.key] === "off"} value={form.values[f.key] === "off" ? "" : (form.values[f.key] ?? "")} oninput={(e) => set(f.key, e.currentTarget.value)} placeholder={f.placeholder || "default"} aria-invalid={err ? "true" : undefined} data-testid="tf-{f.key}" />
        <label class="check small"><input type="checkbox" checked={form.values[f.key] === "off"} onchange={(e) => set(f.key, e.currentTarget.checked ? "off" : "")} data-testid="tf-{f.key}-off" /> off (wet only)</label>
        {#if err}<small class="err" role="alert">{err}</small>{/if}
      </div>
    {:else if f.type === "number" || f.type === "integer" || f.type === "bar"}
      <label class="field">{f.label}{#if f.min !== undefined && f.max !== undefined} <span class="muted">{f.min}–{f.max}</span>{/if}
        <input type="number" inputmode={f.type === "number" ? "decimal" : "numeric"} min={f.min} max={f.max} step={f.step} value={form.values[f.key] ?? ""} oninput={(e) => set(f.key, e.currentTarget.value)} placeholder={f.placeholder || "default"} aria-invalid={err ? "true" : undefined} data-testid="tf-{f.key}" />
        {#if err}<small class="err" role="alert">{err}</small>{/if}
      </label>
    {:else}
      <label class="field">{f.label}
        <input type="text" value={form.values[f.key] ?? ""} oninput={(e) => set(f.key, e.currentTarget.value)} placeholder={f.placeholder || "default"} data-testid="tf-{f.key}" />
        {#if err}<small class="err" role="alert">{err}</small>{/if}
      </label>
    {/if}
  {/each}
</div>

<style>
  .fields {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 9.5rem), 1fr));
    gap: var(--sp-3);
    min-width: 0;
  }
  .wide {
    grid-column: 1 / -1;
  }
  .field,
  label {
    display: flex;
    flex-direction: column;
    gap: var(--sp-1);
    min-width: 0;
    font-size: var(--fs-sm);
  }
  input:not([type="checkbox"]),
  select {
    width: 100%;
    min-width: 0;
    box-sizing: border-box;
    min-height: var(--hit);
  }
  .check {
    flex-direction: row;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
  }
  .check input {
    width: 1.4rem;
    height: 1.4rem;
  }
  .small {
    font-size: var(--fs-xs);
  }
  .presets {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .preset {
    min-height: var(--hit);
    flex: 1 1 5rem;
  }
  .preset[aria-pressed="true"] {
    background: var(--accent);
    color: var(--accent-ink);
    border-color: var(--accent);
  }
  .err {
    color: var(--bad);
  }
  .req {
    color: var(--warn, #e0a030);
    font-size: var(--fs-xs);
  }
</style>
