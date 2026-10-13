<script lang="ts">
  import { api } from "../../api/client";
  import { idempotentAction } from "../../api/idem";
  import type { components } from "../../api/schema";
  import { failedRows, uploadRows, type UploadRow } from "../draftForm";

  type Result = components["schemas"]["UploadResult"];
  let jsonl = $state("");
  let collection = $state("");
  let allowPartial = $state(false);
  let busy = $state(false);
  let error = $state("");
  let result = $state<Result | null>(null);
  let action: (() => Promise<{ data?: Result; message?: string }>) | null = null;
  let actionSig = "";

  let rejected = $state<UploadRow[]>([]);
  const rows = $derived(result ? uploadRows(result) : rejected);

  async function pick(e: Event) {
    const f = (e.currentTarget as HTMLInputElement).files?.[0];
    if (f) jsonl = await f.text();
  }

  async function submit(e: Event) {
    e.preventDefault();
    const body = {
      jsonl,
      ...(collection.trim() ? { collection: collection.trim() } : {}),
      ...(allowPartial ? { allow_partial: true } : {}),
    };
    const sig = JSON.stringify(body);
    if (!action || sig !== actionSig) {
      actionSig = sig;
      action = idempotentAction(async (key) => {
        const r = await api.POST("/api/drafts/batch", { params: { header: { "Idempotency-Key": key } }, body });
        if (r.data) return { data: r.data };
        rejected = failedRows(r.error);
        return { message: r.error?.message ?? "Upload failed" };
      });
    }
    busy = true;
    error = "";
    rejected = [];
    try {
      const r = await action();
      if (r.data) result = r.data;
      else {
        error = r.message ?? "Upload failed";
        result = null;
      }
    } catch {
      error = "Upload failed";
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} class="upload" data-testid="upload-form">
  <label>songs.jsonl file <input type="file" accept=".jsonl,.json,.txt" onchange={pick} data-testid="upload-file" /></label>
  <label>Or paste JSONL <textarea rows="10" class="mono" spellcheck="false" bind:value={jsonl} data-testid="upload-text"></textarea></label>
  <label>Collection (optional) <input bind:value={collection} maxlength="80" data-testid="upload-collection" /></label>
  <label class="inline"><input type="checkbox" bind:checked={allowPartial} data-testid="upload-partial" /> Create the clean lines even when others fail</label>
  <button type="submit" disabled={busy || jsonl.trim() === ""} data-testid="upload-submit">Upload</button>
</form>

{#if error}<p role="alert">{error}</p>{/if}
{#if rejected.length > 0}
  <ul data-testid="upload-rejected">
    {#each rejected as r, n (n)}
      <li data-testid="upload-row" data-state={r.state}>line {r.line}: <b>{r.state}</b> {r.text}</li>
    {/each}
  </ul>
{/if}
{#if result}
  <section data-testid="upload-result">
    <h2>Result</h2>
    <p class="muted">{result.created} created</p>
    <ul>
      {#each rows as r, n (n)}
        <li data-testid="upload-row" data-state={r.state}>line {r.line}: <b>{r.state}</b> {r.text}</li>
      {/each}
    </ul>
    {#if result.created > 0}<p><a href="/backlog{result.collection ? '?collection=' + encodeURIComponent(result.collection) : ''}">Open the backlog</a></p>{/if}
  </section>
{/if}

<style>
  .upload {
    display: grid;
    gap: 0.6rem;
    max-width: 48rem;
  }
  label {
    display: grid;
    gap: 0.2rem;
    font-size: 0.85rem;
  }
  label.inline {
    display: flex;
    gap: 0.4rem;
  }
  .mono {
    font-family: ui-monospace, monospace;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
  }
  ul {
    list-style: none;
    padding: 0;
  }
  li[data-state="error"] b {
    color: #e66;
  }
</style>
