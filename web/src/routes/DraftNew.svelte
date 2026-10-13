<script lang="ts">
  import { api } from "../api/client";
  import { idempotentAction } from "../api/idem";
  import { emptyForm, formFromRequest, labelOrUndefined, toRequest, type DraftForm } from "../lib/draftForm";

  let form = $state<DraftForm>(emptyForm());
  let from = $state("");
  let collection = $state("");
  let error = $state("");
  let busy = $state(false);
  let action: (() => Promise<{ id?: string; message?: string }>) | null = null;
  let actionSig = "";

  async function startFrom() {
    error = "";
    const id = from.trim();
    if (!id) return;
    try {
      const r = await api.GET("/api/drafts/{id}", { params: { path: { id } } });
      if (r.data) form = formFromRequest(r.data.request, r.data.label);
      else error = r.response.status === 404 ? "Draft not found" : (r.error?.message ?? "Could not load the draft");
    } catch {
      error = "Could not load the draft";
    }
  }

  async function submit(e: Event) {
    e.preventDefault();
    const label = labelOrUndefined(form);
    const body = {
      request: toRequest(form) as Record<string, never>,
      ...(label ? { label } : {}),
      ...(collection.trim() ? { collection: collection.trim() } : {}),
    };
    const sig = JSON.stringify(body);
    if (!action || sig !== actionSig) {
      actionSig = sig;
      action = idempotentAction(async (key) => {
        const r = await api.POST("/api/drafts", { params: { header: { "Idempotency-Key": key } }, body });
        return r.data ? { id: r.data.id } : { message: r.error?.message ?? "Could not create the draft" };
      });
    }
    busy = true;
    error = "";
    try {
      const r = await action();
      if (r.id) location.assign("/backlog/" + r.id);
      else error = r.message ?? "Could not create the draft";
    } catch {
      error = "Could not create the draft";
    } finally {
      busy = false;
    }
  }
</script>

<p><a href="/backlog">← Backlog</a></p>
<h1>New draft</h1>
{#if error}<p role="alert">{error}</p>{/if}

<div class="from">
  <label>Start from draft id <input bind:value={from} data-testid="new-from" /></label>
  <button type="button" onclick={startFrom} data-testid="new-from-load">Load</button>
</div>

<form class="editor" onsubmit={submit} data-testid="draft-new-form">
  <label>Label <input bind:value={form.label} maxlength="40" data-testid="draft-label" /></label>
  <label>Request id <input bind:value={form.id} data-testid="draft-id" /></label>
  <label>Style <textarea rows="2" bind:value={form.style} data-testid="draft-style-input"></textarea></label>
  <label>Lyrics <textarea rows="14" class="mono" spellcheck="false" bind:value={form.lyrics} data-testid="draft-lyrics"></textarea></label>
  <label>Chain of thought <textarea rows="3" bind:value={form.cot} data-testid="draft-cot"></textarea></label>
  <div class="row">
    <label>Seed <input inputmode="numeric" bind:value={form.seed} data-testid="draft-seed" /></label>
    <label>CFG scale (optional) <input inputmode="decimal" bind:value={form.cfgScale} data-testid="draft-cfg" /></label>
    <label>Collection (optional) <input bind:value={collection} maxlength="80" data-testid="new-collection" /></label>
  </div>
  <details>
    <summary>ABC</summary>
    <textarea rows="6" class="mono" spellcheck="false" bind:value={form.abc} data-testid="draft-abc"></textarea>
  </details>
  <button type="submit" disabled={busy} data-testid="draft-create">Create draft</button>
</form>

<style>
  .editor {
    display: grid;
    gap: 0.6rem;
    max-width: 48rem;
  }
  .from {
    display: flex;
    gap: 0.5rem;
    align-items: end;
    margin-bottom: 1rem;
  }
  label {
    display: grid;
    gap: 0.2rem;
    font-size: 0.85rem;
  }
  .row {
    display: flex;
    gap: 0.75rem;
    flex-wrap: wrap;
  }
  .mono {
    font-family: ui-monospace, monospace;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
  }
</style>
