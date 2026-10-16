<script lang="ts">
  import { onDestroy } from "svelte";
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import {
    debounced,
    formFromRequest,
    isDirty,
    keepMine,
    lineRange,
    lintForm,
    preview,
    saveDraft,
    setDraftStatus,
    type DraftForm,
  } from "../draftForm";
  import LintPanel from "./LintPanel.svelte";

  type Draft = components["schemas"]["DraftDetail"];
  type Lint = components["schemas"]["LintResult"];
  interface Props {
    draft: Draft;
    /** Server state replaced ours (save, reload theirs, archive). */
    onupdate: (d: Draft) => void;
    /** SPA router navigate: an in-app hop must not reload the document (the player lives in it). */
    navigate: (to: string) => void;
  }
  let { draft, onupdate, navigate }: Props = $props();

  const readonly = $derived(!draft.editable || draft.status === "fulfilled");
  /* svelte-ignore state_referenced_locally */
  const initial = formFromRequest(draft.request, draft.label);
  // The parent keys this component by draft id and revision, so the initial value is the whole story.
  /* svelte-ignore state_referenced_locally */
  let form = $state<DraftForm>(formFromRequest(draft.request, draft.label));
  /* svelte-ignore state_referenced_locally */
  let lint = $state<Lint | null>(draft.lint);
  let lintBusy = $state(false);
  let saving = $state(false);
  let error = $state("");
  let conflict = $state(false);
  let lyricsEl = $state<HTMLTextAreaElement | undefined>();
  let seq = 0;

  const dirty = $derived(isDirty(initial, form));
  const json = $derived(preview(form));

  const runLint = debounced(async () => {
    const mine = ++seq;
    lintBusy = true;
    try {
      const r = await lintForm(api, form, draft.id);
      if (mine === seq && r) lint = r;
    } catch {
      /* lint is advisory */
    } finally {
      if (mine === seq) lintBusy = false;
    }
  });
  onDestroy(() => runLint.cancel());

  function edited() {
    if (!readonly) runLint();
  }

  function jump(line: number) {
    if (!lyricsEl) return;
    const [s, e] = lineRange(form.lyrics, line);
    lyricsEl.focus();
    lyricsEl.setSelectionRange(s, e);
  }

  async function save() {
    saving = true;
    error = "";
    try {
      const r = await saveDraft(api, draft.id, draft.revision, form);
      if (r.kind === "ok") onupdate(r.draft);
      else if (r.kind === "conflict") conflict = true;
      else error = r.message;
    } catch {
      error = "Could not save the draft";
    } finally {
      saving = false;
    }
  }

  async function reloadTheirs() {
    conflict = false;
    try {
      const r = await api.GET("/api/drafts/{id}", { params: { path: { id: draft.id } } });
      if (r.data) onupdate(r.data);
      else error = r.error?.message ?? "Could not reload the draft";
    } catch {
      error = "Could not reload the draft";
    }
  }

  async function mine() {
    conflict = false;
    saving = true;
    try {
      const r = await keepMine(api, form, draft.parent_track_id);
      if (r.kind === "ok") navigate("/backlog/" + r.draft.id);
      else if (r.kind === "error") error = r.message;
    } catch {
      error = "Could not create the draft";
    } finally {
      saving = false;
    }
  }

  async function toggleArchive() {
    error = "";
    try {
      const r = await setDraftStatus(api, draft, draft.status === "archived" ? "backlog" : "archived");
      if (r.kind === "ok") onupdate(r.draft);
      else if (r.kind === "conflict") conflict = true;
      else error = r.message;
    } catch {
      error = "Could not update the draft";
    }
  }
</script>

{#if readonly}<p class="muted" data-testid="draft-readonly">Read-only: this draft has been fulfilled.</p>{/if}
{#if error}<p role="alert">{error}</p>{/if}

<form
  class="editor"
  data-testid="draft-editor"
  oninput={edited}
  onsubmit={(e) => {
    e.preventDefault();
    void save();
  }}
>
  <label>Label <input bind:value={form.label} maxlength="40" disabled={readonly} data-testid="draft-label" /></label>
  <label>Request id <input bind:value={form.id} disabled={readonly} data-testid="draft-id" /></label>
  <label>Style <textarea rows="2" bind:value={form.style} disabled={readonly} data-testid="draft-style-input"></textarea></label>
  <label>
    Lyrics
    <textarea rows="18" class="mono" spellcheck="false" bind:this={lyricsEl} bind:value={form.lyrics} disabled={readonly} data-testid="draft-lyrics"></textarea>
  </label>
  <label>Chain of thought <textarea rows="3" bind:value={form.cot} disabled={readonly} data-testid="draft-cot"></textarea></label>
  <div class="row">
    <label>Seed <input inputmode="numeric" bind:value={form.seed} disabled={readonly} data-testid="draft-seed" /></label>
    <label>CFG scale (optional) <input inputmode="decimal" bind:value={form.cfgScale} disabled={readonly} data-testid="draft-cfg" /></label>
  </div>
  <details>
    <summary>ABC</summary>
    <textarea rows="6" class="mono" spellcheck="false" bind:value={form.abc} disabled={readonly} data-testid="draft-abc"></textarea>
  </details>
  {#if !readonly}
    <div class="row">
      <button type="submit" disabled={!dirty || saving} data-testid="draft-save">Save</button>
      <button type="button" onclick={toggleArchive} data-testid="draft-archive">{draft.status === "archived" ? "Restore" : "Archive"}</button>
      {#if dirty}<span class="muted">Unsaved changes</span>{/if}
    </div>
  {/if}
</form>

<LintPanel {lint} busy={lintBusy} onjump={jump} />

<details>
  <summary>Canonical JSON preview</summary>
  <pre data-testid="draft-preview">{json}</pre>
</details>

{#if conflict}
  <div class="dialog" role="alertdialog" aria-modal="true" aria-label="Revision conflict" data-testid="conflict-dialog">
    <p>This draft was changed elsewhere since you opened it.</p>
    <button type="button" onclick={reloadTheirs} data-testid="conflict-reload">Reload theirs</button>
    <button type="button" onclick={mine} data-testid="conflict-keep">Keep mine as a new draft</button>
    <button type="button" onclick={() => (conflict = false)}>Cancel</button>
  </div>
{/if}

<style>
  .editor {
    display: grid;
    gap: 0.6rem;
    max-width: 48rem;
  }
  label {
    display: grid;
    gap: 0.2rem;
    font-size: 0.85rem;
  }
  .row {
    display: flex;
    gap: 0.75rem;
    align-items: end;
    flex-wrap: wrap;
  }
  .mono {
    font-family: ui-monospace, monospace;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
  }
  pre {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .dialog {
    position: fixed;
    z-index: 20; /* above the player bar (z-index 10), which would otherwise cover the buttons */
    inset: auto 1rem 1rem 1rem;
    max-width: 28rem;
    margin: auto;
    padding: 1rem;
    background: #1d1814;
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
  }
</style>
