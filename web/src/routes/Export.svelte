<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import ExportForm from "../lib/components/ExportForm.svelte";
  import {
    createJsonlExport,
    emptySelection,
    exportIdFromSearch,
    exportSearch,
    fetchExport,
    summaryLine,
    validationView,
    type SelectionKind,
  } from "../lib/exportView";

  type Export = components["schemas"]["Export"];
  type Source = components["schemas"]["ExportSource"];

  let form = $state(emptySelection());
  let sources = $state<Source[]>([]);
  let result = $state<Export | null>(null);
  // What produced the shown result: the request's selection kind, or the one recorded in the URL.
  let shown = $state<{ kind: SelectionKind; forBatch: boolean }>({ kind: "source", forBatch: false });
  let busy = $state(false);
  let error = $state("");

  $effect(() => {
    void (async () => {
      try {
        const r = await api.GET("/api/exports/sources", {});
        if (r.data) sources = r.data.items;
      } catch {
        error = "Failed to load export sources";
      }
    })();
  });

  // ?export=<id>: one get_export call opens an earlier result.
  $effect(() => {
    const ref = exportIdFromSearch(window.location.search);
    if (!ref) return;
    void (async () => {
      busy = true;
      const r = await fetchExport(api, ref.id);
      busy = false;
      if (r.ok) {
        result = r.export;
        shown = { kind: ref.kind ?? (r.export.jsonl ? "tracks" : "source"), forBatch: ref.forBatch ?? false };
        error = "";
      } else error = r.message;
    })();
  });

  async function run() {
    if (busy) return;
    busy = true;
    error = "";
    const sel = { kind: form.kind, forBatch: form.kind === "source" && form.forBatch };
    const r = await createJsonlExport(api, form);
    busy = false;
    if (r.ok) {
      result = r.export;
      shown = sel;
      history.replaceState(null, "", window.location.pathname + exportSearch(r.export.id, sel));
    } else error = r.message;
  }

  // Replace a signed link that is about to expire.
  $effect(() => {
    const cur = result;
    if (!cur) return;
    const wait = Date.parse(cur.download.expires_at) - Date.now() - 60_000;
    const h = setTimeout(
      async () => {
        const r = await fetchExport(api, cur.id);
        if (r.ok) result = r.export;
        else error = r.message;
      },
      Math.min(Math.max(wait, 5000), 2_000_000_000),
    );
    return () => clearTimeout(h);
  });

  const info = $derived(result?.jsonl ?? null);
  const verdict = $derived(info ? validationView(info.validation, shown) : null);
</script>

<h1>Export</h1>
{#if error}<p role="alert" data-testid="export-error">{error}</p>{/if}

<ExportForm bind:value={form} {sources} {busy} onsubmit={run} />

{#if result}
  <section class="result" data-testid="export-result">
    <h2>Result</h2>
    {#if info}
      <p data-testid="export-summary">{summaryLine(info)}</p>
      {#if verdict}
        <div class="card {verdict.tone}" data-testid="validator-card" data-tone={verdict.tone}>
          <b>Validator: {verdict.label}</b>
          {#if info.validation.issues.length > 0}
            <ul>
              {#each info.validation.issues as i, k (k)}
                <li data-code={i.code}>{i.code}{i.line ? ` (line ${i.line})` : ""}{i.field ? ` · ${i.field}` : ""}</li>
              {/each}
            </ul>
          {/if}
        </div>
      {/if}
      {#if info.renamed.length > 0}
        <h3>Renamed ids</h3>
        <ul data-testid="export-renamed">
          {#each info.renamed as r (r.from + r.to)}<li>{r.from} → {r.to}</li>{/each}
        </ul>
      {/if}
      {#if info.warnings.length > 0}
        <h3>Warnings</h3>
        <ul>
          {#each info.warnings as w, k (k)}<li data-code={w.code}>{w.code}: {w.message}</li>{/each}
        </ul>
      {/if}
      {#if info.abc_files.length > 0}
        <p class="muted">{info.abc_files.length} ABC {info.abc_files.length === 1 ? "file" : "files"} are referenced by the JSONL.</p>
      {/if}
    {/if}
    <p>
      <a href={result.download.url} download={result.filename} data-testid="export-download">Download {result.filename}</a>
      <span class="muted">{result.bytes} bytes · sha256 <code data-testid="export-sha">{result.sha256}</code></span>
    </p>
    {#if info?.content}
      <details>
        <summary>Preview</summary>
        <pre data-testid="export-content">{info.content}</pre>
      </details>
    {/if}
  </section>
{/if}

<style>
  .result {
    margin-top: 1.5rem;
  }
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
    margin: 0.5rem 0;
  }
  .card.ok {
    border-color: #3f7d4f;
  }
  .card.failed {
    border-color: #a14444;
  }
  .card.original {
    border-color: #a08a3c;
  }
  pre {
    overflow: auto;
    max-height: 24rem;
  }
</style>
