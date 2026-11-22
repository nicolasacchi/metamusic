<script lang="ts">
  // The bar editor of a plan (DESK-W1, C363.2): replace bars, transpose bars or set the tempo, each through edit_plan. The
  // plan never changes; a new plan comes back and the page opens it. The structure (voices, bar counts, headers) is the
  // server's to judge: its refusal is shown in words, with the violating bars listed.
  import { untrack } from "svelte";
  import { api as defaultApi } from "../../api/client";
  import { editPlan, type DeskFailure, type Plan } from "../deskApi";
  import type { BarRef } from "../abcRender";
  import {
    barText, buildEdit, editRefusalText, emptyEdit, sectionOf, violationsOf, voiceIds, type EditForm, type EditMode,
  } from "../deskView";

  interface Props {
    plan: Pick<Plan, "id" | "abc" | "bar_spans" | "bars" | "score" | "sections">;
    selected?: BarRef | null;
    /** A new plan was made (created) or the same result already existed (not created). */
    onedited: (plan: Plan, created: boolean) => void;
    client?: typeof defaultApi;
  }
  let { plan, selected = null, onedited, client = defaultApi }: Props = $props();

  let form = $state<EditForm>(emptyEdit());
  let busy = $state(false);
  let refusal = $state<DeskFailure | null>(null);
  let touched = $state(false);

  const voices = $derived(voiceIds(plan));
  const built = $derived(buildEdit(form, plan.bars));

  // Selecting a bar fills the form with it (and the bar's current text when replacing).
  $effect(() => {
    const s = selected;
    if (!s) return;
    untrack(() => {
      form.voice = s.voice;
      form.from = String(s.bar + 1);
      form.to = String(s.bar + 1);
      if (form.mode === "replace") form.abc = barText(plan.abc ?? "", plan.bar_spans, s.voice, s.bar)?.trim() ?? "";
      refusal = null;
    });
  });

  const setMode = (m: EditMode) => {
    form.mode = m;
    refusal = null;
    touched = false;
    if (m === "replace" && form.abc === "" && selected) form.abc = barText(plan.abc ?? "", plan.bar_spans, selected.voice, selected.bar)?.trim() ?? "";
  };
  const section = $derived.by(() => {
    const n = Number(form.from);
    return Number.isInteger(n) && n >= 1 ? sectionOf(plan, n - 1) : null;
  });

  async function apply() {
    touched = true;
    if (busy || built.ops === null) return;
    busy = true;
    refusal = null;
    const r = await editPlan(client, plan.id, built.ops, built.rationale || undefined)();
    busy = false;
    if (r.ok) onedited(r.value.plan, r.value.created);
    else refusal = r;
  }
  const err = (k: string) => (touched ? built.errors[k] : undefined);
</script>

<section class="card" data-testid="bar-editor" aria-labelledby="bar-editor-h">
  <h2 id="bar-editor-h">Edit bars</h2>
  <p class="muted">{selected ? `Bar ${selected.bar + 1}${selected.voice ? ` of voice ${selected.voice}` : ""} is selected.` : "Click a note in the score to pick its bar."} An edit makes a new plan; this one stays as it is.</p>
  <div class="seg" role="group" aria-label="What to do">
    <button type="button" aria-pressed={form.mode === "replace"} onclick={() => setMode("replace")} data-testid="edit-mode-replace">Replace bars</button>
    <button type="button" aria-pressed={form.mode === "transpose"} onclick={() => setMode("transpose")} data-testid="edit-mode-transpose">Transpose</button>
    <button type="button" aria-pressed={form.mode === "tempo"} onclick={() => setMode("tempo")} data-testid="edit-mode-tempo">Tempo</button>
  </div>
  {#if form.mode === "tempo"}
    <label>Tempo (BPM)
      <input inputmode="numeric" value={form.bpm} oninput={(e) => (form.bpm = e.currentTarget.value)} aria-invalid={err("bpm") ? "true" : undefined} data-testid="edit-bpm" />
    </label>
    {#if err("bpm")}<small class="err" role="alert">{err("bpm")}</small>{/if}
  {:else}
    <div class="fields">
      <label>Voice{form.mode === "transpose" ? " (blank: every voice)" : ""}
        <select value={form.voice} onchange={(e) => (form.voice = e.currentTarget.value)} data-testid="edit-voice">
          <option value="">{form.mode === "transpose" ? "Every voice" : "Choose…"}</option>
          {#each voices as v (v)}<option value={v}>{v}</option>{/each}
        </select>
      </label>
      <label>From bar
        <input inputmode="numeric" value={form.from} oninput={(e) => (form.from = e.currentTarget.value)} aria-invalid={err("from") ? "true" : undefined} data-testid="edit-from" />
      </label>
      <label>To bar
        <input inputmode="numeric" value={form.to} oninput={(e) => (form.to = e.currentTarget.value)} aria-invalid={err("to") ? "true" : undefined} data-testid="edit-to" />
      </label>
    </div>
    {#each ["voice", "from", "to"] as k (k)}{#if err(k)}<small class="err" role="alert">{err(k)}</small>{/if}{/each}
    {#if section}<p class="muted">Section: {section.label}</p>{/if}
    {#if form.mode === "replace"}
      <label class="wide">New bars (ABC, with the bar lines between them)
        <textarea rows="3" spellcheck="false" value={form.abc} oninput={(e) => (form.abc = e.currentTarget.value)} aria-invalid={err("abc") ? "true" : undefined} data-testid="edit-abc"></textarea>
      </label>
      {#if err("abc")}<small class="err" role="alert">{err("abc")}</small>{/if}
    {:else}
      <label>Semitones (negative: down)
        <input inputmode="numeric" value={form.semitones} oninput={(e) => (form.semitones = e.currentTarget.value)} aria-invalid={err("semitones") ? "true" : undefined} data-testid="edit-semitones" />
      </label>
      {#if err("semitones")}<small class="err" role="alert">{err("semitones")}</small>{/if}
    {/if}
  {/if}
  <label>Why (optional)
    <input value={form.rationale} oninput={(e) => (form.rationale = e.currentTarget.value)} data-testid="edit-rationale" />
  </label>
  {#if refusal}
    <div class="err" role="alert" data-testid="edit-refusal">
      <p>{editRefusalText(refusal)}</p>
      {#if violationsOf(refusal).length > 0}
        <ul data-testid="edit-violations">
          {#each violationsOf(refusal) as v, k (k)}<li>{v.voice ? `Voice ${v.voice}, ` : ""}bar {v.bar}: {v.msg}</li>{/each}
        </ul>
      {/if}
    </div>
  {/if}
  <button type="button" class="apply" disabled={busy} onclick={() => void apply()} data-testid="edit-apply">{busy ? "Saving…" : "Make an edited plan"}</button>
</section>

<style>
  .card {
    display: grid;
    gap: var(--sp-3);
    min-width: 0;
    max-width: 100%;
    box-sizing: border-box;
  }
  .seg {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .seg button {
    min-height: var(--hit);
    flex: 1 1 auto;
  }
  .seg button[aria-pressed="true"] {
    background: var(--accent);
    color: var(--accent-ink);
    border-color: var(--accent);
  }
  label {
    display: flex;
    flex-direction: column;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
    min-width: 0;
  }
  input,
  select,
  textarea {
    min-height: var(--hit);
    max-width: 100%;
    box-sizing: border-box;
    font: inherit;
  }
  textarea {
    font-family: var(--font-mono, monospace);
    min-height: 5rem;
  }
  .fields {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(8rem, 1fr));
    gap: var(--sp-3);
  }
  .apply {
    min-height: var(--hit);
  }
</style>
