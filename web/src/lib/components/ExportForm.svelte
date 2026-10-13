<script lang="ts">
  import type { components } from "../../api/schema";
  import type { SelectionForm } from "../exportView";

  type Source = components["schemas"]["ExportSource"];
  interface Props {
    value: SelectionForm;
    sources: readonly Source[];
    busy?: boolean;
    onsubmit: () => void;
  }
  let { value = $bindable(), sources, busy = false, onsubmit }: Props = $props();

  const trackStatuses = ["draft", "to_relisten", "final", "discarded"];
  const draftStatuses = ["backlog", "fulfilled", "archived"];

  function toggle(list: string[], v: string): string[] {
    return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
  }
  const canSubmit = $derived(value.kind !== "source" || value.sourcePath !== "");
</script>

<form
  data-testid="export-form"
  onsubmit={(e) => {
    e.preventDefault();
    onsubmit();
  }}
>
  <fieldset>
    <legend>What to export</legend>
    <label><input type="radio" name="sel" value="source" bind:group={value.kind} /> A source file</label>
    <label><input type="radio" name="sel" value="tracks" bind:group={value.kind} /> Tracks</label>
    <label><input type="radio" name="sel" value="drafts" bind:group={value.kind} /> Drafts</label>
  </fieldset>

  {#if value.kind === "source"}
    <label>
      Source file
      <select bind:value={value.sourcePath} data-testid="export-source">
        <option value="">Choose…</option>
        {#each sources as s (s.path)}
          <option value={s.path}>{s.path} ({s.lines} lines, {s.recipes} recipes)</option>
        {/each}
      </select>
    </label>
    {#if sources.length === 0}<p class="muted">No imported JSONL files yet.</p>{/if}
    <label><input type="checkbox" bind:checked={value.forBatch} data-testid="export-for-batch" /> Make ids unique for <code>yue2 batch</code></label>
  {:else if value.kind === "tracks"}
    <fieldset>
      <legend>Status</legend>
      {#each trackStatuses as s (s)}
        <label><input type="checkbox" checked={value.trackStatus.includes(s)} onchange={() => (value.trackStatus = toggle(value.trackStatus, s))} /> {s}</label>
      {/each}
    </fieldset>
    <label>Minimum stars <input type="number" min="1" max="5" bind:value={value.minStars} /></label>
    <label>Tags (comma separated) <input bind:value={value.tags} /></label>
    <label>Playlist id <input bind:value={value.playlistId} /></label>
  {:else}
    <fieldset>
      <legend>Status</legend>
      {#each draftStatuses as s (s)}
        <label><input type="checkbox" checked={value.draftStatus.includes(s)} onchange={() => (value.draftStatus = toggle(value.draftStatus, s))} /> {s}</label>
      {/each}
    </fieldset>
    <label>Collection <input bind:value={value.collection} /></label>
    <label>Origin <input bind:value={value.origin} /></label>
    <label>Group key <input bind:value={value.groupKey} /></label>
  {/if}

  <label>File name (optional) <input bind:value={value.filename} maxlength="120" /></label>
  <button type="submit" disabled={busy || !canSubmit} data-testid="export-submit">{busy ? "Exporting…" : "Export"}</button>
</form>

<style>
  form {
    display: grid;
    gap: 0.6rem;
    max-width: 36rem;
  }
  label {
    display: block;
  }
  fieldset label {
    display: inline-block;
    margin-right: 1rem;
  }
</style>
