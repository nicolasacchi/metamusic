<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import DraftEditor from "../lib/components/DraftEditor.svelte";
  import { draftsIntent, mayGenerate, startGeneration, type Viewer } from "../lib/genIntent";
  import { fetchViewer } from "../lib/viewer";

  type Draft = components["schemas"]["DraftDetail"];
  let { id, navigate }: { id: string; navigate: (to: string) => void } = $props();

  let draft = $state<Draft | null>(null);
  let error = $state("");
  let me = $state<Viewer | null>(null);

  $effect(() => {
    void fetchViewer().then((v) => (me = v));
  });

  function generate(draftId: string) {
    startGeneration(sessionStorage, draftsIntent([draftId]), (to) => location.assign(to));
  }

  $effect(() => {
    const mine = id;
    draft = null;
    error = "";
    void (async () => {
      try {
        const r = await api.GET("/api/drafts/{id}", { params: { path: { id: mine } } });
        if (mine !== id) return;
        if (r.data) draft = r.data;
        else error = r.response.status === 404 ? "Draft not found" : (r.error?.message ?? "Failed to load draft");
      } catch {
        if (mine === id) error = "Failed to load draft";
      }
    })();
  });
</script>

<p><a href="/backlog">← Backlog</a></p>
{#if error}<p role="alert">{error}</p>{/if}
{#if draft}
  <h1>{draft.label ?? draft.request_id}</h1>
  <p class="muted" data-testid="draft-meta">
    {draft.request_id} · {draft.status} · {draft.origin}{#if draft.group_key} · group {draft.group_key}{/if}
  </p>
  {#if draft.collections.length}
    <p>{#each draft.collections as c (c)}<span class="badge">{c}</span>{/each}</p>
  {/if}
  {#if mayGenerate(me) && draft.status !== "archived"}
    <p><button type="button" onclick={() => generate(draft?.id ?? "")} data-testid="generate-draft">Generate this draft</button></p>
  {/if}
  {#if draft.fulfilled_by_track_id}
    <p>Fulfilled by <a href="/tracks/{draft.fulfilled_by_track_id}">this track</a>.</p>
  {/if}

  {#key draft.id + ":" + draft.revision + ":" + draft.status}
    <DraftEditor {draft} {navigate} onupdate={(d) => (draft = d)} />
  {/key}

  {#if draft.reference}
    <h2>Reference</h2>
    <dl data-testid="draft-reference">
      {#each Object.entries(draft.reference) as [k, v] (k)}
        <dt>{k}</dt>
        <dd>{v}</dd>
      {/each}
    </dl>
  {/if}

  {#if draft.group.length > 1}
    <h2>Group</h2>
    <ul data-testid="draft-group">
      {#each draft.group as m (m.id)}
        <li>
          {#if m.id === draft.id}<b>{m.label ?? m.request_id}</b>{:else}<a href="/backlog/{m.id}">{m.label ?? m.request_id}</a>{/if}
          <span class="muted">{m.status}</span>
        </li>
      {/each}
    </ul>
  {/if}
{:else if !error}
  <p class="muted">Loading…</p>
{/if}

<style>
  .badge {
    font-size: 0.7rem;
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0 0.4rem;
    margin-right: 0.3rem;
  }
  dd {
    margin: 0 0 0.5rem;
    overflow-wrap: anywhere;
  }
</style>
